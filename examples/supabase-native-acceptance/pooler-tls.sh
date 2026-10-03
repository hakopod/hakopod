#!/bin/sh
set -eu

: "${KUBECONFIG:?set the protected development kubeconfig}"
: "${HAKOPOD_SUPABASE_NAMESPACE:?set the disposable managed-platform namespace}"
: "${HAKOPOD_SUPAVISOR_MANIFEST_DIGEST:=sha256:91930deeb066e948a287f74e680388e7e54d397005f430c2e3e71f36799d07ec}"

kubectl_bin=${KUBECTL_BIN:-kubectl}
context=k3d-hakopod-dev
[ "$($kubectl_bin --request-timeout=15s --kubeconfig "$KUBECONFIG" config current-context)" = "$context" ]

k() {
  "$kubectl_bin" --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" -n "$HAKOPOD_SUPABASE_NAMESPACE" "$@"
}

pooler=$(k get pod -l app.kubernetes.io/component=pooler -o jsonpath='{.items[0].metadata.name}')
studio=$(k get pod -l app.kubernetes.io/component=studio -o jsonpath='{.items[0].metadata.name}')
[ -n "$pooler" ] && [ -n "$studio" ]

image_id=$(k get pod "$pooler" -o jsonpath='{.status.containerStatuses[0].imageID}')
case "$image_id" in
  *"$HAKOPOD_SUPAVISOR_MANIFEST_DIGEST") ;;
  *) echo "pooler does not run the reviewed manifest: $image_id" >&2; exit 1 ;;
esac

k exec "$pooler" -- /bin/sh -ceu '
  [ "$(id -u):$(id -g)" = "65534:65534" ]
  test -r "$DATABASE_SSL_CA_CERT"
  test "$(stat -c %a "$DATABASE_SSL_CA_CERT")" = 440
  /app/bin/supavisor eval '\''
    tenant = Supavisor.Tenants.get_tenant_by_external_id(System.fetch_env!("POOLER_TENANT_ID"))
    true = tenant.upstream_ssl
    :peer = tenant.upstream_verify
    "db" = tenant.sni_hostname
    %{rows: [[true, version, cipher]]} = Supavisor.Repo.query!("select ssl, version, cipher from pg_stat_ssl where pid=pg_backend_pid()")
    true = version in ["TLSv1.2", "TLSv1.3"]
    IO.puts("repo_tls=" <> version <> ":" <> cipher)
  '\''
  PGPASSWORD="$POSTGRES_PASSWORD" PGSSLMODE=disable psql \
    -h 127.0.0.1 -p 5432 -U "postgres.$POOLER_TENANT_ID" -d "$POSTGRES_DB" \
    -v ON_ERROR_STOP=1 -Atqc "select current_database(), current_user"

  wrong_ca=/tmp/hakopod-wrong-ca.crt
  wrong_key=/tmp/hakopod-wrong-ca.key
  trap '\''rm -f "$wrong_ca" "$wrong_key"'\'' EXIT HUP INT TERM
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=hakopod-wrong-ca \
    -keyout "$wrong_key" -out "$wrong_ca" >/dev/null 2>&1
  /app/bin/supavisor eval '\''
    base = Ecto.Repo.Supervisor.parse_url(System.fetch_env!("DATABASE_URL"))
    connect = fn server_name, ca ->
      Postgrex.start_link(
        base
        |> Keyword.put(:ssl, true)
        |> Keyword.put(:ssl_opts, Supavisor.TLS.peer_options(server_name, cacertfile: ca))
        |> Keyword.put(:backoff_type, :stop)
        |> Keyword.put(:connect_timeout, 3_000)
        |> Keyword.put(:sync_connect, true)
      )
    end
    {:ok, pid} = connect.("db", System.fetch_env!("DATABASE_SSL_CA_CERT"))
    %{rows: [[true, version]]} = Postgrex.query!(pid, "select ssl, version from pg_stat_ssl where pid=pg_backend_pid()", [])
    true = version in ["TLSv1.2", "TLSv1.3"]
    GenServer.stop(pid)
    {:error, wrong_ca} = connect.("db", "/tmp/hakopod-wrong-ca.crt")
    wrong_ca_evidence = inspect(wrong_ca)
    true = String.contains?(wrong_ca_evidence, ["unknown_ca", "Unknown CA", "bad_cert"])
    {:error, wrong_host} = connect.("wrong-db", System.fetch_env!("DATABASE_SSL_CA_CERT"))
    wrong_host_evidence = inspect(wrong_host)
    true = String.contains?(wrong_host_evidence, ["hostname_check_failed", "does not match", "bad_cert"])
    IO.puts("adversarial_tls=wrong_ca_rejected:wrong_host_rejected")
  '\''

  application_token=/tmp/hakopod-application.jwt
  admin_token=/tmp/hakopod-pooler-admin.jwt
  trap '\''rm -f "$wrong_ca" "$wrong_key" "$application_token" "$admin_token"'\'' EXIT HUP INT TERM
  /app/bin/supavisor eval '\''
    claims = %{"role" => "service_role"}
    File.write!("/tmp/hakopod-application.jwt", Supavisor.Jwt.Token.gen!(claims, System.fetch_env!("METRICS_JWT_SECRET")))
    File.write!("/tmp/hakopod-pooler-admin.jwt", Supavisor.Jwt.Token.gen!(claims, System.fetch_env!("API_JWT_SECRET")))
    File.chmod!("/tmp/hakopod-application.jwt", 0o600)
    File.chmod!("/tmp/hakopod-pooler-admin.jwt", 0o600)
  '\''
  request_status() {
    token_file=$1
    {
      printf '\''header = "authorization: Bearer '\''
      cat "$token_file"
      printf '\''"\n'\''
    } | curl --config - --silent --output /dev/null --write-out "%{http_code}" --max-time 3 \
      "http://127.0.0.1:4000/api/tenants/$POOLER_TENANT_ID"
  }
  application_status=$(request_status "$application_token")
  admin_status=$(request_status "$admin_token")
  [ "$application_status" = 403 ]
  [ "$admin_status" = 200 ]
  echo "admin_authority=application_rejected:pooler_admin_accepted"
'

# Supavisor uses independent administrative key material, and application
# workloads must also remain unable to reach the listener. Studio has the
# service-role token but its policy allows only meta and api-gw egress. A
# successful HTTP response is a failure, regardless of status.
if k exec "$studio" -- node -e '
  const c = new AbortController();
  setTimeout(() => c.abort(), 3000);
  fetch("http://supavisor:4000/api/tenants/" + process.env.DEFAULT_PROJECT_NAME, {
    headers: {authorization: "Bearer " + process.env.SUPABASE_SERVICE_KEY},
    signal: c.signal
  }).then(r => { console.error("unexpected Supavisor response", r.status); process.exit(41) })
    .catch(() => process.exit(0));
'; then :; else
  rc=$?
  [ "$rc" -ne 41 ] || { echo "application token reached Supavisor admin API" >&2; exit 1; }
  exit "$rc"
fi

k exec "$pooler" -- /app/bin/supavisor eval '
  tenant = Supavisor.Tenants.get_tenant_by_external_id(System.fetch_env!("POOLER_TENANT_ID"))
  :peer = tenant.upstream_verify
  "db" = tenant.sni_hostname
  IO.puts("tenant_tls_policy=peer:db")
'

# Attribute the pooler's live database sessions without printing credentials.
k exec statefulset/supabase-database -- /bin/sh -ceu '
  psql -U postgres -v ON_ERROR_STOP=1 -Atc "
    select coalesce(a.application_name, '\'''\''), s.ssl, s.version, s.cipher
    from pg_stat_activity a
    join pg_stat_ssl s using (pid)
    where a.client_addr is not null
      and (a.application_name = '\''supavisor_meta'\'' or a.usename = '\''pgbouncer'\'')
    order by a.application_name, a.pid"
' | awk -F '|' '
  BEGIN { found=0; bad=0 }
  { if ($2!="t" || ($3!="TLSv1.2" && $3!="TLSv1.3") || $4=="") bad=1; else found=1 }
  END { exit found && !bad ? 0 : 1 }
'

echo "native Supavisor positive TLS and application-isolation checks passed"
