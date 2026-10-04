#!/bin/sh
set -eu
umask 077
: "${HAKOPOD_BEHAVIOR_BASE_URL:?set the loopback-forwarded HTTPS base URL}"
: "${HAKOPOD_BEHAVIOR_HOST:?set the reviewed gateway hostname}"
: "${HAKOPOD_BEHAVIOR_PORT:?set the local port-forward port}"
: "${HAKOPOD_BEHAVIOR_CA:?set the reviewed gateway CA path}"
: "${HAKOPOD_BEHAVIOR_ANON_HEADERS:?set the protected anon header file}"
: "${HAKOPOD_BEHAVIOR_OWNER_HEADERS:?set the protected owner header file}"
: "${HAKOPOD_BEHAVIOR_SERVICE_HEADERS:?set the protected service header file}"
: "${HAKOPOD_BEHAVIOR_EVIDENCE_DIR:?set the protected evidence directory}"
case ${HAKOPOD_BEHAVIOR_EVIDENCE_DIR} in /*/hakopod-supabase-native-*/evidence) ;; *) echo 'invalid behavior evidence directory' >&2; exit 2;; esac
[ -d "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR" ] && [ ! -L "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR" ]
for file in "$HAKOPOD_BEHAVIOR_CA" "$HAKOPOD_BEHAVIOR_ANON_HEADERS" "$HAKOPOD_BEHAVIOR_OWNER_HEADERS" "$HAKOPOD_BEHAVIOR_SERVICE_HEADERS"; do [ -f "$file" ] && [ ! -L "$file" ] || { echo 'missing behavior input' >&2; exit 2; }; done
curl_tls() { curl --noproxy "$HAKOPOD_BEHAVIOR_HOST" --resolve "$HAKOPOD_BEHAVIOR_HOST:$HAKOPOD_BEHAVIOR_PORT:127.0.0.1" --cacert "$HAKOPOD_BEHAVIOR_CA" --max-time 10 --max-filesize 1048576 "$@"; }
write_json() { output=$1; shift; [ ! -e "$output" ] && [ ! -L "$output" ] || { echo 'behavior evidence output already exists' >&2; exit 2; }; jq -n "$@" >"$output"; chmod 600 "$output"; }
mode=${1:?choose auth, image, edge or studio}
case $mode in
 auth)
  headers=$(mktemp /tmp/hakopod-supabase-auth-headers.XXXXXX);body=$(mktemp /tmp/hakopod-supabase-auth-body.XXXXXX);request=$(mktemp /tmp/hakopod-supabase-auth-request.XXXXXX);trap 'rm -f "$headers" "$body" "$request"' EXIT HUP INT TERM
  python3 - "$request" <<'PY'
import json,pathlib,secrets,sys
suffix=secrets.token_hex(8)
pathlib.Path(sys.argv[1]).write_text(json.dumps({'type':'signup','email':'hakopod-redirect-'+suffix+'@example.invalid','password':'Hakopod-test-only-'+secrets.token_urlsafe(24)+'-Aa1!','redirect_to':'https://attacker.invalid/callback'}))
PY
  status=$(curl_tls --silent --show-error --dump-header "$headers" --output "$body" --write-out '%{http_code}' -X POST "$HAKOPOD_BEHAVIOR_BASE_URL/auth/v1/admin/generate_link" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS" -H 'content-type: application/json' --data-binary @"$request")
  case $status in 200|201) ;; *) echo 'administrative signup-link generation failed' >&2; exit 1;; esac
  jq -e '.action_link|type=="string" and length>0' "$body" >/dev/null
  ! awk 'BEGIN{IGNORECASE=1} /^location:/ && /attacker\.invalid/{bad=1} END{exit bad?0:1}' "$headers" || { echo 'unapproved auth redirect was returned' >&2; exit 1; }
  ! jq -e '(.redirect_to // "")|contains("attacker.invalid")' "$body" >/dev/null || { echo 'unapproved auth redirect was accepted' >&2; exit 1; }
  ! jq -e '.action_link|contains("attacker.invalid")' "$body" >/dev/null || { echo 'unapproved auth redirect entered the action link' >&2; exit 1; }
  write_json "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/auth-redirect-and-signup.json" '{admin_signup_link_created:true,unapproved_redirect_refused:true}'
 ;;
 image)
  : "${HAKOPOD_BEHAVIOR_BUCKET:?set the disposable storage bucket}"
  original=$(mktemp "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/.image-original.XXXXXX");rendered=$(mktemp "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/.image-rendered.XXXXXX");trap 'rm -f "$original" "$rendered"' EXIT HUP INT TERM
  python3 - "$original" <<'PY'
import pathlib,struct,sys,zlib
def chunk(kind,data):
    return struct.pack('>I',len(data))+kind+data+struct.pack('>I',zlib.crc32(kind+data)&0xffffffff)
raw=b''.join((b'\x00'+b'\xff\x00\x00\xff'*2) for _ in range(2))
pathlib.Path(sys.argv[1]).write_bytes(b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('>IIBBBBB',2,2,8,6,0,0,0))+chunk(b'IDAT',zlib.compress(raw))+chunk(b'IEND',b''))
PY
  object=hakopod-transform-$$.png
  curl_tls --fail --silent --show-error -X POST "$HAKOPOD_BEHAVIOR_BASE_URL/storage/v1/object/$HAKOPOD_BEHAVIOR_BUCKET/$object" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS" -H 'content-type: image/png' --data-binary @"$original" >/dev/null
  cleanup_image() { curl_tls --silent --output /dev/null -X DELETE "$HAKOPOD_BEHAVIOR_BASE_URL/storage/v1/object/$HAKOPOD_BEHAVIOR_BUCKET" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS" -H 'content-type: application/json' --data "{\"prefixes\":[\"$object\"]}" || true; rm -f "$original" "$rendered"; }
  trap cleanup_image EXIT HUP INT TERM
  curl_tls --fail --silent --show-error "$HAKOPOD_BEHAVIOR_BASE_URL/storage/v1/render/image/authenticated/$HAKOPOD_BEHAVIOR_BUCKET/$object?width=1&height=1&resize=cover&format=origin" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS" --output "$rendered"
  python3 - "$rendered" <<'PY'
import pathlib,struct,sys
data=pathlib.Path(sys.argv[1]).read_bytes()
if len(data)>1048576 or data[:8]!=b'\x89PNG\r\n\x1a\n' or struct.unpack('>II',data[16:24])!=(1,1):raise SystemExit(1)
PY
  original_sha=$(sha256sum "$original"|awk '{print $1}');rendered_sha=$(sha256sum "$rendered"|awk '{print $1}');[ "$original_sha" != "$rendered_sha" ]
  curl_tls --fail --silent --show-error -X DELETE "$HAKOPOD_BEHAVIOR_BASE_URL/storage/v1/object/$HAKOPOD_BEHAVIOR_BUCKET" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS" -H 'content-type: application/json' --data "{\"prefixes\":[\"$object\"]}" | jq -e 'type=="array" and length==1' >/dev/null
  removed=$(curl_tls --silent --output /dev/null --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/storage/v1/object/info/authenticated/$HAKOPOD_BEHAVIOR_BUCKET/$object" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS");[ "$removed" = 400 ]
  rm -f "$original" "$rendered";trap - EXIT HUP INT TERM
  write_json "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/image-transformation.json" --arg original "$original_sha" --arg rendered "$rendered_sha" '{source_sha256:$original,rendered_sha256:$rendered,width:1,height:1}'
 ;;
 edge)
  unauth=$(curl_tls --silent --output /dev/null --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/functions/v1/hello")
  [ "$unauth" = 401 ] || { echo 'edge function accepted an unauthenticated request' >&2; exit 1; }
  invalid_body="$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/edge-invalid-token.body"
  invalid_headers="$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/edge-invalid-token.headers"
  invalid=$(curl_tls --silent --show-error --dump-header "$invalid_headers" --output "$invalid_body" --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/functions/v1/hello" --header 'authorization: Bearer invalid')
  [ "$invalid" = 401 ] || { echo 'edge function accepted a malformed bearer token' >&2; exit 1; }
  tr -d '\r' <"$invalid_headers" | grep -Eiq '^sb-error-code:[[:space:]]*UNAUTHORIZED_INVALID_JWT_FORMAT$' || { echo 'edge function omitted the malformed-token error header' >&2; exit 1; }
  jq -e '.code=="UNAUTHORIZED_INVALID_JWT_FORMAT"' "$invalid_body" >/dev/null
  curl_tls --fail --silent --show-error "$HAKOPOD_BEHAVIOR_BASE_URL/functions/v1/hello" --header @"$HAKOPOD_BEHAVIOR_OWNER_HEADERS" | jq -e '. == {message:"Hello from Edge Functions!"}' >/dev/null
  rm -f "$invalid_body" "$invalid_headers"
  write_json "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/edge-runtime-isolation.json" --argjson unauthenticated "$unauth" --argjson invalid "$invalid" '{unauthenticated_status:$unauthenticated,invalid_token_status:$invalid,invalid_token_code:"UNAUTHORIZED_INVALID_JWT_FORMAT",authenticated_fixture:true}'
 ;;
 studio)
  : "${HAKOPOD_BEHAVIOR_STUDIO_HEADERS:?set the protected Studio Basic Auth header file}"
  [ -f "$HAKOPOD_BEHAVIOR_STUDIO_HEADERS" ] && [ ! -L "$HAKOPOD_BEHAVIOR_STUDIO_HEADERS" ] || { echo 'missing Studio behavior input' >&2; exit 2; }
  root_status=$(curl_tls --silent --output /dev/null --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/");service_status=$(curl_tls --silent --output /dev/null --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/" --header @"$HAKOPOD_BEHAVIOR_SERVICE_HEADERS");mcp_status=$(curl_tls --silent --output /dev/null --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/mcp")
  admin_status=$(curl_tls --silent --output /dev/null --write-out '%{http_code}' "$HAKOPOD_BEHAVIOR_BASE_URL/" --header @"$HAKOPOD_BEHAVIOR_STUDIO_HEADERS")
  [ "$root_status" = 401 ] && [ "$service_status" = 401 ] && [ "$mcp_status" = 403 ] || { echo 'studio administrative isolation failed' >&2; exit 1; }
  [ "$admin_status" = 200 ] || { echo 'Studio Basic Auth did not grant reviewed access' >&2;exit 1; }
  write_json "$HAKOPOD_BEHAVIOR_EVIDENCE_DIR/studio-admin-isolation.json" --argjson root "$root_status" --argjson service "$service_status" --argjson mcp "$mcp_status" --argjson admin "$admin_status" '{anonymous_status:$root,service_role_status:$service,mcp_status:$mcp,basic_auth_status:$admin}'
 ;;
 *) echo 'unknown Supabase behavior check' >&2;exit 2;;
esac
