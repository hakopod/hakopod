#!/usr/bin/env python3
"""Fixed Neon native acceptance orchestration; evidence comes only from live probes."""
import argparse, hashlib, json, os, re, resource, socket, ssl, struct, subprocess, sys, tempfile, time, urllib.error, urllib.parse, urllib.request
from pathlib import Path

ID = re.compile(r"^[0-9a-f]{32}$")
DIGEST = re.compile(r"^[0-9a-f]{64}$")
COMPONENTS = {"broker", "compute", "compute-tls", "controller-database", "pageserver", "proxy", "safekeeper", "storage-controller"}
MAX_COMMAND_OUTPUT = 2 << 20
MAX_COMMAND_INPUT = 1 << 20

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

def load(path, maximum=2 << 20):
    path = Path(path); info = path.lstat()
    if path.is_symlink() or not path.is_file() or not 1 < info.st_size <= maximum: raise RuntimeError("input is missing, symbolic, or unbounded")
    raw = path.read_bytes()
    if len(raw) > maximum: raise RuntimeError("input exceeded its bound")
    return json.loads(raw)

def protected_text(path, maximum=4096):
    path = Path(path); info = path.lstat()
    if path.is_symlink() or not path.is_file() or info.st_mode & 0o077 or not 0 < info.st_size <= maximum: raise RuntimeError("protected input is missing, symbolic, permissive, or unbounded")
    raw = path.read_bytes()
    if len(raw) > maximum or b"\x00" in raw: raise RuntimeError("protected input exceeded its bound")
    return raw.decode("utf-8")

def atomic(path, value):
    path = Path(path)
    if path.exists() or path.is_symlink(): raise RuntimeError("evidence path is not fresh")
    temporary = path.with_name("." + path.name + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(value, output, sort_keys=True, separators=(",", ":")); output.write("\n"); output.flush(); os.fsync(output.fileno())
        os.link(temporary, path)
    finally: temporary.unlink(missing_ok=True)

def validate_attestation(a):
    value=load(a.gate_attestation)
    required={"schema_version","run_id","kind","project","environment","context","cluster_uid","node_uids","kubeconfig","source_root","source_files","binary_sha256","expires_at"}
    if set(value)!=required or value["schema_version"]!=1 or not ID.fullmatch(str(value["run_id"])) or value["kind"]!="neon" or value["project"]!=a.project or value["environment"]!="development" or value["context"]!="k3d-hakopod-dev": raise RuntimeError("gate attestation identity is invalid")
    if Path(value["source_root"]).resolve()!=Path(a.source).resolve() or Path(value["kubeconfig"]).resolve()!=Path(a.kubeconfig).resolve() or not DIGEST.fullmatch(str(value["binary_sha256"])): raise RuntimeError("gate attestation paths or binary identity differ")
    files=value["source_files"]
    if not isinstance(files,dict) or not 10<=len(files)<=8192: raise RuntimeError("gate source inventory is incomplete")
    for relative,expected in files.items():
        path=Path(relative)
        if path.is_absolute() or path==Path(".") or ".." in path.parts or not DIGEST.fullmatch(str(expected)): raise RuntimeError("gate source identity is malformed")
        absolute=Path(a.source)/path
        if absolute.is_symlink() or not absolute.is_file() or hashlib.sha256(absolute.read_bytes()).hexdigest()!=expected: raise RuntimeError("gate source identity changed")
    nodes=value["node_uids"]
    if not isinstance(nodes,dict) or not 3<=len(nodes)<=8 or len(set(nodes.values()))!=len(nodes) or not value["cluster_uid"]: raise RuntimeError("gate cluster identity is incomplete")
    return value

def validate_contract(a):
    if sys.platform != "linux" or os.uname().machine not in ("x86_64", "amd64"): raise RuntimeError("native acceptance requires Linux AMD64")
    work = Path(a.work_dir)
    if not work.is_absolute() or work.exists() or work.is_symlink() or work.parent not in (Path("/tmp"), Path("/srv/hakopod-backup-scratch")) or not work.name.startswith("hakopod-neon-native-"): raise RuntimeError("work path is not fresh and bounded")
    api = urllib.parse.urlsplit(a.api_url)
    if api.scheme != "http" or api.hostname not in ("127.0.0.1", "localhost") or api.port is None or not 1 <= api.port <= 65535 or api.username is not None or api.password is not None or api.path not in ("", "/") or api.query or api.fragment: raise RuntimeError("API is not an exact loopback origin")
    validate_attestation(a)
    source, target, cancellation, images, identities = load(a.source_spec), load(a.target_spec), load(a.cancellation_target_spec), load(a.images), load(a.identities)
    for spec in (source, target, cancellation):
        neon = spec.get("neon", {})
        if spec.get("schema_version") != 1 or spec.get("kind") != "neon" or neon.get("safekeepers") != 3 or not 2 <= neon.get("pageservers", 0) <= 8 or not 1 <= neon.get("compute_replicas", 0) <= 6: raise RuntimeError("strict Neon topology is required")
    if len({spec.get("name") for spec in (source,target,cancellation)}) != 3 or len({spec["neon"].get("object_storage_prefix") for spec in (source,target,cancellation)}) != 3: raise RuntimeError("restore targets are not separate")
    if set(images) != COMPONENTS or any(not re.fullmatch(r"[^@]+@sha256:[0-9a-f]{64}", v) for v in images.values()): raise RuntimeError("image inventory is not exact and pinned")
    if set(identities) != COMPONENTS or any(set(v) != {"uid", "gid", "image"} or type(v["uid"]) is not int or type(v["gid"]) is not int or min(v["uid"], v["gid"]) <= 0 or v["image"] != images[name] for name, v in identities.items()): raise RuntimeError("identity inventory is incomplete")
    if not ID.fullmatch(a.destination_id) or a.destination_revision < 1: raise RuntimeError("destination identity is malformed")
    return source, target, cancellation

class Driver:
    def __init__(self, a):
        self.a=a; self.source_spec,self.target_spec,self.cancellation_target_spec=validate_contract(a); self.root=Path(a.work_dir); self.root.mkdir(mode=0o700); self.evidence=self.root/"evidence"; self.evidence.mkdir(mode=0o700)
        token=protected_text(a.token_file)
        if token != token.strip() or "\n" in token or "\r" in token: raise RuntimeError("API token is malformed")
        self.token=token; self.platforms=[]; self.bound={}; self.run_id=""; self.foreign=None; self.native={}; self.pending_revocation=None; self.pending_lifecycle=None
        if not 16 <= len(self.token) <= 4096: raise RuntimeError("API token is malformed")
        password=protected_text(a.proxy_password_file)
        if password!=password.strip() or any(char in password for char in "\r\n"): raise RuntimeError("proxy SQL credential is malformed")
        if not Path(a.psql).is_absolute() or not os.access(a.psql,os.X_OK): raise RuntimeError("PostgreSQL client executable is unavailable")
    def command(self, argv, timeout=30, data=None, env=None):
        if not isinstance(argv, (list, tuple)) or not 1 <= len(argv) <= 128 or any(not isinstance(item, str) or not item or "\x00" in item or len(item.encode()) > 8192 for item in argv): raise RuntimeError("acceptance command is malformed")
        if type(timeout) not in (int, float) or not 1 <= timeout <= 1800: raise RuntimeError("acceptance command timeout is invalid")
        if data is not None and (not isinstance(data, bytes) or len(data) > MAX_COMMAND_INPUT): raise RuntimeError("acceptance command input is invalid or unbounded")
        def limits(): resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_COMMAND_OUTPUT + 1, MAX_COMMAND_OUTPUT + 1))
        with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            process = subprocess.Popen(argv, stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL, stdout=stdout, stderr=stderr, close_fds=True, preexec_fn=limits, env=env)
            try: process.communicate(data, timeout=timeout)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=5)
                raise RuntimeError("bounded acceptance command timed out")
            stdout.seek(0, os.SEEK_END); out_size=stdout.tell(); stderr.seek(0, os.SEEK_END); err_size=stderr.tell()
            if out_size > MAX_COMMAND_OUTPUT or err_size > MAX_COMMAND_OUTPUT or process.returncode: raise RuntimeError("bounded acceptance command failed; output withheld")
            stdout.seek(0); raw=stdout.read(MAX_COMMAND_OUTPUT + 1)
        try: return raw.decode("utf-8")
        except UnicodeDecodeError as error: raise RuntimeError("acceptance command output is not UTF-8") from error
    def k(self,*argv,timeout=30): return self.command(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev",*argv],timeout)
    def api(self,method,path,body=None,idem=None,expected=(200,202)):
        parsed = urllib.parse.urlsplit(path)
        if method not in ("GET", "POST") or not path.startswith("/api/v1/") or parsed.scheme or parsed.netloc or parsed.query or parsed.fragment or ".." in parsed.path.split("/") or len(path) > 2048: raise RuntimeError("API request path is invalid")
        raw=None if body is None else json.dumps(body,separators=(",",":")).encode(); headers={"authorization":"Bearer "+self.token,"accept":"application/json"}
        if raw is not None and len(raw) > 1 << 20: raise RuntimeError("API request body exceeded its bound")
        if raw is not None: headers["content-type"]="application/json"
        if idem:
            if not isinstance(idem, str) or not 8 <= len(idem) <= 128 or any(ord(char) < 33 or ord(char) > 126 for char in idem): raise RuntimeError("idempotency key is invalid")
            headers["idempotency-key"]=idem
        req=urllib.request.Request(self.a.api_url.rstrip("/")+path,data=raw,headers=headers,method=method)
        try:
            with HTTP.open(req,timeout=15) as response:
                if response.geturl() != req.full_url: raise RuntimeError("API redirect was refused")
                data,status=response.read((1<<20)+1),response.status
        except urllib.error.HTTPError as error: data,status=error.read((1<<20)+1),error.code
        if len(data)>1<<20 or status not in expected: raise RuntimeError("API returned an unexpected bounded response")
        return json.loads(data) if data else {}
    def wait(self,path,seconds=900,allowed=("succeeded",)):
        deadline=time.monotonic()+seconds
        while time.monotonic()<deadline:
            item=self.api("GET",path)
            if item.get("status") in {"succeeded","failed","cancelled"}:
                if item["status"] not in allowed: raise RuntimeError("operation terminal state was not accepted")
                return item
            time.sleep(2)
        raise RuntimeError("operation exceeded its time bound")
    def wait_phase(self,path,phases,seconds=300):
        deadline=time.monotonic()+seconds
        while time.monotonic()<deadline:
            item=self.api("GET",path)
            if item.get("phase") in phases: return item
            if item.get("status") in {"succeeded","failed","cancelled"}: raise RuntimeError("operation became terminal before the required phase")
            time.sleep(0.1)
        raise RuntimeError("operation did not reach the required phase")
    def create(self,spec):
        body={"project":self.a.project,"environment":"development","expected_revision":0,"kind":"create","spec":spec}; reviewed=self.api("POST","/api/v1/managed-platforms/reviews",body)
        if reviewed.get("blocked") is not False or not reviewed.get("review",{}).get("id"): raise RuntimeError("native capability gate remains closed")
        platform=reviewed["platform"]
        if platform.get("project")!=self.a.project or platform.get("environment")!="development" or platform.get("spec")!=spec or reviewed["review"].get("expected_revision")!=0: raise RuntimeError("create review did not echo the exact request")
        self.command([sys.executable,self.a.control_plane_bridge,"allow","--kubeconfig",self.a.kubeconfig,"--platform-id",platform["id"]],240)
        request={"id":platform["id"],"project":platform["project"],"environment":platform["environment"],"expected_revision":0,"kind":"create","spec":platform["spec"],"review":reviewed["review"]}
        op=self.api("POST","/api/v1/managed-platforms/operations",request,"neon-create-"+platform["id"]); self.platforms.append((platform["id"],spec["name"])); return platform,self.wait("/api/v1/managed-platform-operations/"+op["id"])
    def delete(self,platform_id,name):
        current=self.api("GET","/api/v1/managed-platforms/"+platform_id); body={"id":platform_id,"project":self.a.project,"environment":"development","expected_revision":current["revision"],"kind":"delete","confirm_name":name,"spec":current["spec"]}
        reviewed=self.api("POST","/api/v1/managed-platforms/reviews",body); body["review"]=reviewed["review"]; op=self.api("POST","/api/v1/managed-platforms/operations",body,"neon-delete-"+platform_id); self.wait("/api/v1/managed-platform-operations/"+op["id"])
    def recover(self,intent,label):
        review=self.api("POST","/api/v1/managed-platform-recovery/reviews",intent); op=self.api("POST","/api/v1/managed-platform-recovery/operations",dict(intent,review=review),"neon-"+label+"-"+self.run_id); return self.wait("/api/v1/managed-platform-recovery-operations/"+op["id"],1200)
    def namespace(self,pid): return json.loads(self.k("get","namespace","managed-platform-"+pid,"-o","json"))
    def pods(self,pid):
        ns="managed-platform-"+pid; pods=json.loads(self.k("-n",ns,"get","pods","-l","hakopod.io/managed-platform-id="+pid,"-o","json"))["items"]
        if not pods or len(pods)>32: raise RuntimeError("pod inventory is incomplete")
        return ns,pods
    def record(self,case,value):
        path=self.evidence/(case+".json"); atomic(path,value); self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"record","--state",str(self.evidence/"state.json"),"--events",str(self.evidence/"events.json"),"--case",case,"--observation",str(path)])
    def ownership(self,pid,opid):
        ns="managed-platform-"+pid; namespace=self.namespace(pid); labels=namespace["metadata"].get("labels",{})
        if labels.get("hakopod.io/owner-operation-id")!=opid: raise RuntimeError("namespace ownership differs from operation")
        raw=self.k("-n",ns,"get","all,configmap,secret,pvc,networkpolicy","-o","jsonpath={range .items[*]}{.metadata.labels.hakopod\\.io/resource-intent-id}{'\\n'}{end}")
        intents=sorted({x for x in raw.splitlines() if ID.fullmatch(x)})
        if not 3<=len(intents)<=16: raise RuntimeError("resource intent inventory is incomplete")
        sentinel="hakopod-neon-sentinel-"+self.run_id[:12]
        manifest=json.dumps({"apiVersion":"v1","kind":"Namespace","metadata":{"name":sentinel,"labels":{"hakopod.io/native-acceptance-run":self.run_id}}}).encode(); self.command(["kubectl","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","create","-f","-"],data=manifest)
        sentinel_uid=self.namespace_uid(sentinel); name="foreign-neon-"+self.run_id[:12]; manifest=json.dumps({"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":name,"namespace":sentinel},"data":{"owner":"foreign"}}).encode(); self.command(["kubectl","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","create","-f","-"],data=manifest); self.foreign=(sentinel,sentinel_uid,name); time.sleep(3)
        if json.loads(self.k("-n",sentinel,"get","configmap",name,"-o","json")).get("data")!={"owner":"foreign"}: raise RuntimeError("foreign resource changed")
        current=self.api("GET","/api/v1/managed-platforms/"+pid)
        probe=self.api("POST","/api/v1/managed-platforms/"+pid+"/native-probe",{"expected_revision":current["revision"]})
        if probe.get("platform_id")!=pid or probe.get("platform_revision")!=current["revision"] or probe.get("namespace_uid")!=namespace["metadata"]["uid"] or probe.get("ownership_capability_verified") is not True: raise RuntimeError("native provider identity probe was not verified")
        self.native[pid]=probe
        if any(probe.get(field) is not True for field in ("wrong_owner_refused","wrong_deletion_token_refused")): raise RuntimeError("provider ownership rejection was not observed")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":namespace["metadata"]["uid"],"mutation_capability_required":probe["ownership_capability_verified"],"foreign_owner_refused":probe["wrong_owner_refused"],"deletion_token_required":probe["wrong_deletion_token_refused"],"owner_operation_id":opid,"resource_intent_ids":intents}
    def runtime(self,pid,uid):
        ns,pods=self.pods(pid); roles={p["metadata"]["labels"].get("hakopod.io/neon-role") for p in pods}; required={"storage-controller","pageserver","safekeeper","compute","proxy"}
        if not required<=roles: raise RuntimeError("runtime role inventory is incomplete")
        services=json.loads(self.k("-n",ns,"get","services","-o","json"))["items"]
        service_names={s.get("metadata",{}).get("name") for s in services}
        expected_services={"neon-storage-controller","neon-proxy"}|{"neon-pageserver-"+str(i) for i in range(self.source_spec["neon"]["pageservers"])}|{"neon-safekeeper-"+str(i) for i in range(3)}|{"neon-compute-"+str(i) for i in range(self.source_spec["neon"]["compute_replicas"])}|{"neon-compute-"+str(i)+"-control" for i in range(self.source_spec["neon"]["compute_replicas"])}
        if not expected_services<=service_names: raise RuntimeError("service inventory is incomplete")
        compute=next(p for p in pods if p["metadata"]["labels"].get("hakopod.io/neon-role")=="compute"); containers={c["name"] for c in compute["spec"]["containers"]}
        if not {"compute","compute-tls"}<=containers: raise RuntimeError("compute TLS sidecar is absent")
        probe=self.native.get(pid,{}); computes=probe.get("compute_names"); tls_observed=probe.get("tls",{})
        if any(tls_observed.get(field) is not True for field in ("client_verification_enforced","server_verified","plaintext_refused")) or set(tls_observed.get("services",[]))!=required: raise RuntimeError("five-service provider TLS evidence is missing")
        expected_computes=["compute-"+str(i) for i in range(self.source_spec["neon"]["compute_replicas"])]
        if not ID.fullmatch(str(probe.get("tenant_id"))) or not ID.fullmatch(str(probe.get("timeline_id"))) or type(probe.get("tenant_generation")) is not int or type(probe.get("timeline_generation")) is not int or sorted(computes or [])!=expected_computes: raise RuntimeError("native durable lifecycle identity is missing")
        tls={"run_id":self.run_id,"platform_id":pid,"namespace_uid":uid,"client_verification_enforced":tls_observed["client_verification_enforced"],"server_verified":tls_observed["server_verified"],"plaintext_refused":tls_observed["plaintext_refused"],"services":sorted(tls_observed["services"])}
        lifecycle={"run_id":self.run_id,"platform_id":pid,"namespace_uid":uid,"tenant_id":probe["tenant_id"],"timeline_id":probe["timeline_id"],"tenant_generation":probe["tenant_generation"],"timeline_generation":probe["timeline_generation"],"compute_names":computes,"created":True,"stopped":False,"deleted":False}
        return tls,lifecycle
    def managed_tls(self,pid,revision,operation_id):
        namespace="managed-platform-"+pid; ca=self.root/"managed-tls-ca.crt"; injected_path=self.evidence/"managed-tls-injected.json"
        common=[sys.executable,self.a.managed_tls_helper,"--kubectl","kubectl","--kubeconfig",self.a.kubeconfig,"--namespace",namespace,"--platform-id",pid,"--platform-revision",str(revision),"--owner-operation-id",operation_id,"--proxy-host","neon-proxy","--api-url",self.a.api_url.rstrip("/"),"--api-token-file",self.a.token_file,"--project",self.a.project,"--environment","development","--ca-file",str(ca),"--private-tmp",str(self.root),"--spec-digest-helper",self.a.runtime_spec_digest_helper,"--psql-command-file",self.a.control_psql_command_file,"--openssl",self.a.openssl,"--local-port",str(self.a.local_port)]
        errors=tempfile.TemporaryFile()
        forward=subprocess.Popen(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","-n",namespace,"port-forward","service/neon-proxy",str(self.a.local_port)+":5432"],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=errors,close_fds=True)
        try:
            deadline=time.monotonic()+30
            while time.monotonic()<deadline:
                if forward.poll() is not None: raise RuntimeError("Neon proxy port-forward exited")
                try:
                    with socket.create_connection(("127.0.0.1",self.a.local_port),timeout=.2): break
                except OSError: time.sleep(.2)
            else: raise RuntimeError("Neon proxy port-forward did not become ready")
            before=json.loads(self.command([*common,"observe"],60))
            injected=json.loads(self.command([*common,"inject"],180))
            atomic(injected_path,injected)
            renewed=json.loads(self.command([*common,"await-renewal","--input",str(injected_path)],480))
        finally:
            forward.terminate()
            try: forward.wait(timeout=5)
            except subprocess.TimeoutExpired:
                forward.kill(); forward.wait(timeout=5)
            errors.close()
        return {"before":before,"injection":injected,"renewal":renewed}
    def proxy_auth_message(self,namespace,target,local_port,ca,platform_id):
        errors=tempfile.TemporaryFile()
        forward=subprocess.Popen(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","-n",namespace,"port-forward",target,str(local_port)+":5432"],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=errors,close_fds=True)
        try:
            for _ in range(150):
                if forward.poll() is not None: raise RuntimeError("Neon proxy trust port-forward exited")
                try:
                    raw=socket.create_connection(("127.0.0.1",local_port),timeout=.2); break
                except OSError: time.sleep(.2)
            else: raise RuntimeError("Neon proxy trust port-forward did not become ready")
            try:
                raw.sendall(struct.pack("!II",8,80877103))
                if raw.recv(1)!=b"S": raise RuntimeError("Neon proxy trust probe refused TLS")
                context=ssl.create_default_context(cafile=str(ca))
                with context.wrap_socket(raw,server_hostname="neon-proxy") as connection:
                    payload=struct.pack("!I",196608)+b"user\x00cloud_admin\x00database\x00postgres\x00options\x00endpoint="+platform_id.encode()+b"\x00\x00"
                    connection.sendall(struct.pack("!I",len(payload)+4)+payload)
                    connection.settimeout(10)
                    return connection.recv(1)
            finally: raw.close()
        finally:
            forward.terminate()
            try:forward.wait(timeout=5)
            except subprocess.TimeoutExpired:
                forward.kill(); forward.wait(timeout=5)
            errors.close()
    def control_plane_trust(self,pid):
        namespace="managed-platform-"+pid; deployment=json.loads(self.k("-n",namespace,"get","deployment","neon-proxy","-o","json"))
        secrets=[volume.get("secret",{}).get("secretName") for volume in deployment["spec"]["template"]["spec"]["volumes"] if volume.get("name")=="proxy-auth"]
        if len(secrets)!=1 or not re.fullmatch(r"platform-tls-proxy-[0-9a-f]{16}-r1",secrets[0] or ""): raise RuntimeError("active Neon proxy TLS snapshot is ambiguous")
        base=["--kubeconfig",self.a.kubeconfig,"--platform-id",pid]
        self.command([sys.executable,self.a.control_plane_bridge,"probe-start",*base,"--proxy-secret",secrets[0],"--ca-kind","correct"],240)
        try: correct=self.proxy_auth_message(namespace,"deployment/hakopod-neon-wrong-ca",self.a.local_port+1,self.root/"managed-tls-ca.crt",pid)
        finally:self.command([sys.executable,self.a.control_plane_bridge,"probe-stop",*base],240)
        if correct!=b"R": raise RuntimeError("correct control-plane issuer did not reach PostgreSQL authentication")
        self.command([sys.executable,self.a.control_plane_bridge,"probe-start",*base,"--proxy-secret",secrets[0],"--ca-kind","wrong"],240)
        try:
            wrong=self.proxy_auth_message(namespace,"deployment/hakopod-neon-wrong-ca",self.a.local_port+1,self.root/"managed-tls-ca.crt",pid)
            self.command([sys.executable,self.a.control_plane_bridge,"probe-verify-wrong",*base],60)
        finally:self.command([sys.executable,self.a.control_plane_bridge,"probe-stop",*base],240)
        if wrong!=b"E": raise RuntimeError("wrong control-plane issuer was not rejected")
        self.authenticated_proxy_query(pid)
        return {"correct_issuer_reached_authentication":True,"wrong_issuer_refused":True,"proxy_authenticated_query":True}
    def authenticated_proxy_query(self,pid):
        if not ID.fullmatch(pid): raise RuntimeError("proxy SQL endpoint identity is invalid")
        password=protected_text(self.a.proxy_password_file)
        if password!=password.strip() or any(char in password for char in "\r\n"): raise RuntimeError("proxy SQL credential is malformed")
        environment={key:value for key,value in os.environ.items() if not key.startswith("PG") and key!="PSQLRC"}
        ca=self.root/"managed-tls-ca.crt"
        environment.update(PGPASSWORD=password,PGOPTIONS="endpoint="+pid,PGCONNECT_TIMEOUT="10",PGHOST="neon-proxy",PGHOSTADDR="127.0.0.1",PGPORT=str(self.a.local_port),PGUSER="cloud_admin",PGDATABASE="postgres",PGSSLMODE="verify-full",PGSSLROOTCERT=str(ca))
        errors=tempfile.TemporaryFile()
        forward=subprocess.Popen(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","-n","managed-platform-"+pid,"port-forward","service/neon-proxy",str(self.a.local_port)+":5432"],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=errors,close_fds=True)
        try:
            for _ in range(150):
                if forward.poll() is not None: raise RuntimeError("Neon proxy SQL port-forward exited")
                try:
                    with socket.create_connection(("127.0.0.1",self.a.local_port),timeout=.2): break
                except OSError: time.sleep(.2)
            else: raise RuntimeError("Neon proxy SQL port-forward did not become ready")
            observed=self.command([self.a.psql,"-XAtw","-v","ON_ERROR_STOP=1","-c","SELECT current_user || ':' || current_database()"],45,env=environment).strip()
            if observed!="cloud_admin:postgres": raise RuntimeError("Neon proxy SQL authenticated identity differs")
        finally:
            forward.terminate()
            try:forward.wait(timeout=5)
            except subprocess.TimeoutExpired:
                forward.kill(); forward.wait(timeout=5)
            errors.close()
    def sql(self,ns,pod,statement):
        return self.command(["kubectl","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","-n",ns,"exec",pod,"-c","compute","--","psql","-XAt","postgresql://cloud_admin@127.0.0.1:55433/postgres?sslmode=require","-v","ON_ERROR_STOP=1","-c",statement],45).strip()
    def compute_pod(self,pid):
        ns,pods=self.pods(pid); matches=[p for p in pods if p["metadata"]["labels"].get("hakopod.io/neon-role")=="compute" and all(x.get("ready") for x in p.get("status",{}).get("containerStatuses",[]))]
        if len(matches)!=self.source_spec["neon"]["compute_replicas"]: raise RuntimeError("ready compute inventory is incomplete")
        matches.sort(key=lambda item:item["metadata"]["name"]); return ns,matches[0]["metadata"]["name"]
    def recovery_receipt(self,value,operation,artifact,source,source_revision,source_uid,target,target_revision):
        required={"operation_id","status","artifact_id","manifest_sha256","source_platform_id","source_revision","source_namespace_uid","target_platform_id","target_revision","format","parts","neon"}
        if set(value)!=required or value.get("operation_id")!=operation or value.get("status")!="succeeded" or value.get("artifact_id")!=artifact or not DIGEST.fullmatch(str(value.get("manifest_sha256"))) or value.get("source_platform_id")!=source or value.get("source_revision")!=source_revision or value.get("source_namespace_uid")!=source_uid or value.get("target_platform_id")!=target or value.get("target_revision")!=target_revision or value.get("format")!="hakopod-neon-recovery-v1" or value.get("parts")!=["tenant.json","timeline.json","remote-storage.tar"] or not isinstance(value.get("neon"),dict): raise RuntimeError("native recovery receipt binding is invalid")
        return value["neon"]
    def cancellation_receipt(self,value,operation,artifact,manifest,source,source_revision,target,target_revision,target_uid):
        required={"schema_version","operation_id","project","environment","source_platform_id","source_revision","target_platform_id","target_revision","artifact_id","manifest_sha256","tenant_id","timeline_id","tenant_generation","timeline_generation","journal_entries","journal_phase_counts","cleanup_pending","operation_authority_refused","namespace_uid","deployment_count","statefulset_count","workload_replicas_zero","pods_absent","staging_prefix_empty"}
        counts=value.get("journal_phase_counts")
        topology=self.cancellation_target_spec["neon"]; expected_sets=topology["compute_replicas"]+topology["pageservers"]+topology["safekeepers"]
        if set(value)!=required or value.get("schema_version")!=1 or value.get("operation_id")!=operation or value.get("project")!=self.a.project or value.get("environment")!="development" or value.get("source_platform_id")!=source or value.get("source_revision")!=source_revision or value.get("target_platform_id")!=target or value.get("target_revision")!=target_revision or value.get("artifact_id")!=artifact or value.get("manifest_sha256")!=manifest or value.get("namespace_uid")!=target_uid or not ID.fullmatch(str(value.get("tenant_id"))) or not ID.fullmatch(str(value.get("timeline_id"))) or type(value.get("tenant_generation")) is not int or value["tenant_generation"]<1 or type(value.get("timeline_generation")) is not int or value["timeline_generation"]<1 or type(value.get("journal_entries")) is not int or value["journal_entries"]<1 or not isinstance(counts,dict) or set(counts)!={"complete","empty_complete","untouched_complete"} or any(type(count) is not int or count<0 for count in counts.values()) or sum(counts.values())!=value["journal_entries"] or value.get("cleanup_pending") is not False or value.get("operation_authority_refused") is not True or value.get("deployment_count")!=1 or value.get("statefulset_count")!=expected_sets or value.get("workload_replicas_zero") is not True or value.get("pods_absent") is not True or value.get("staging_prefix_empty") is not True: raise RuntimeError("native cancellation receipt binding is invalid")
        return value
    def restart(self,pid,uid):
        ns,pods=self.pods(pid); chosen={p["metadata"]["labels"].get("hakopod.io/neon-role"):p for p in pods if p["metadata"]["labels"].get("hakopod.io/neon-role") in ("pageserver","compute")}
        before={k:v["metadata"]["uid"] for k,v in chosen.items()}
        if set(before)!={"pageserver","compute"}: raise RuntimeError("restart subjects are missing")
        seed="hakopod-"+self.run_id; self.sql(ns,chosen["compute"]["metadata"]["name"],"CREATE TABLE IF NOT EXISTS hakopod_native_acceptance(k text primary key,v text not null); INSERT INTO hakopod_native_acceptance VALUES ('restart','"+seed+"') ON CONFLICT (k) DO UPDATE SET v=excluded.v;")
        for pod in chosen.values(): self.k("-n",ns,"delete","pod",pod["metadata"]["name"],"--wait=false")
        after={}; failure_observed=False; deadline=time.monotonic()+300
        while time.monotonic()<deadline:
            _,current=self.pods(pid)
            current_uids={pod["metadata"]["uid"] for pod in current}; failure_observed = failure_observed or any(uid not in current_uids for uid in before.values())
            for pod in current:
                c=pod["metadata"]["labels"].get("hakopod.io/neon-role"); ready=all(x.get("ready") for x in pod.get("status",{}).get("containerStatuses",[]))
                if c in before and pod["metadata"]["uid"]!=before[c] and ready: after[c]=pod
            if set(after)==set(before): break
            time.sleep(2)
        if set(after)!=set(before): raise RuntimeError("owned pods did not recover")
        restored=self.sql(ns,after["compute"]["metadata"]["name"],"SELECT v FROM hakopod_native_acceptance WHERE k='restart';")
        if restored!=seed or not failure_observed: raise RuntimeError("restart failure and restored SQL data were not both observed")
        digest=hashlib.sha256(restored.encode()).hexdigest()
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":uid,"storage_pod_uids_before":[before["pageserver"]],"storage_pod_uids_after":[after["pageserver"]["metadata"]["uid"]],"compute_pod_uids_before":[before["compute"]],"compute_pod_uids_after":[after["compute"]["metadata"]["uid"]],"failure_observed":failure_observed,"service_recovered":restored==seed,"data_sha256":digest}
    def run(self):
        attestation=validate_attestation(self.a); nodes=json.loads(self.k("get","nodes","-o","json"))["items"]
        observed_nodes={item["metadata"]["name"]:item["metadata"]["uid"] for item in nodes}; cluster_uid=self.namespace_uid("kube-system")
        if observed_nodes!=attestation["node_uids"] or cluster_uid!=attestation["cluster_uid"]: raise RuntimeError("attested cluster identity changed")
        self.run_id=self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"begin","--source",self.a.source,"--images",self.a.images,"--identities",self.a.identities,"--state",str(self.evidence/"state.json"),"--events",str(self.evidence/"events.json"),"--context","k3d-hakopod-dev"]).strip()
        source,sop=self.create(self.source_spec); target,top=self.create(self.target_spec)
        for role,platform,op in (("source",source,sop),("recovery_target",target,top)):
            namespace=self.namespace(platform["id"]); self.bound[platform["id"]]=(namespace["metadata"]["uid"],op["id"])
            path=self.evidence/(role+"-create.json"); atomic(path,{k:op[k] for k in ("id","platform_id","kind","status")}); self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"bind-resource","--state",str(self.evidence/"state.json"),"--role",role,"--operation",str(path)])
        self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"observe-identities","--state",str(self.evidence/"state.json")],180)
        source_uid=self.namespace(source["id"])["metadata"]["uid"]; self.record("ownership-capability",self.ownership(source["id"],sop["id"])); tls,lifecycle=self.runtime(source["id"],source_uid); source_current=self.api("GET","/api/v1/managed-platforms/"+source["id"]); tls["managed_tls"]=self.managed_tls(source["id"],source_current["revision"],sop["id"]); tls["control_plane_trust"]=self.control_plane_trust(source["id"]); self.record("tls",tls); self.pending_lifecycle=lifecycle
        source_ns,source_compute=self.compute_pod(source["id"]); seed="backup-"+self.run_id; self.sql(source_ns,source_compute,"CREATE TABLE IF NOT EXISTS hakopod_native_acceptance(k text primary key,v text not null); INSERT INTO hakopod_native_acceptance VALUES ('backup','"+seed+"') ON CONFLICT (k) DO UPDATE SET v=excluded.v;")
        sc=self.api("GET","/api/v1/managed-platforms/"+source["id"]); tc=self.api("GET","/api/v1/managed-platforms/"+target["id"])
        backup=self.recover({"kind":"backup","project":self.a.project,"environment":"development","source_platform_id":source["id"],"destination_id":self.a.destination_id,"destination_revision":self.a.destination_revision,"expected_source_revision":sc["revision"]},"backup"); artifact=backup.get("result_artifact_id")
        if not ID.fullmatch(str(artifact)): raise RuntimeError("backup did not publish an artifact")
        intent={"kind":"restore","project":self.a.project,"environment":"development","source_platform_id":source["id"],"target_platform_id":target["id"],"artifact_id":artifact,"expected_source_revision":sc["revision"],"expected_target_revision":tc["revision"],"confirm_target_name":self.target_spec["name"]}; restored=self.recover(intent,"restore")
        receipt=self.api("GET","/api/v1/managed-platform-recovery-operations/"+restored["id"]+"/native-receipt"); neon=self.recovery_receipt(receipt,restored["id"],artifact,source["id"],sc["revision"],source_uid,target["id"],tc["revision"]); target_uid=self.namespace(target["id"])["metadata"]["uid"]
        target_probe=self.api("POST","/api/v1/managed-platforms/"+target["id"]+"/native-probe",{"expected_revision":tc["revision"]})
        if target_probe.get("platform_id")!=target["id"] or target_probe.get("platform_revision")!=tc["revision"] or target_probe.get("namespace_uid")!=target_uid or target_uid==source_uid or target_probe.get("tenant_id")!=neon.get("tenant_id") or target_probe.get("timeline_id")!=neon.get("timeline_id"): raise RuntimeError("restored target provider identity is not isolated or artifact-bound")
        target_ns,target_compute=self.compute_pod(target["id"]); restored_value=self.sql(target_ns,target_compute,"SELECT v FROM hakopod_native_acceptance WHERE k='backup';")
        if restored_value!=seed: raise RuntimeError("restored target SQL did not match the pre-backup source value")
        evidence={"run_id":self.run_id,"platform_id":source["id"],"namespace_uid":source_uid,"target_platform_id":target["id"],"target_namespace_uid":target_uid,"format":receipt.get("format"),"parts":receipt.get("parts"),"tenant_id":neon.get("tenant_id"),"timeline_id":neon.get("timeline_id"),"tenant_generation":neon.get("tenant_generation"),"timeline_generation":neon.get("timeline_generation"),"commit_lsn":neon.get("commit_lsn"),"pageserver_remote_consistent_lsns":neon.get("pageserver_remote_consistent_lsns"),"source_object_prefix":neon.get("source_object_prefix"),"object_inventory_sha256":neon.get("object_inventory_sha256"),"object_count":neon.get("object_count"),"object_bytes":neon.get("object_bytes"),"restored_data_sha256":hashlib.sha256(restored_value.encode()).hexdigest(),"isolated_target":receipt.get("target_platform_id")==target["id"] and target_uid!=source_uid}; self.record("backup-recovery",evidence); self.record("restart-failure",self.restart(target["id"],target_uid))
        cancellation,cop=self.create(self.cancellation_target_spec); cancellation_uid=self.namespace(cancellation["id"])["metadata"]["uid"]; self.bound[cancellation["id"]]=(cancellation_uid,cop["id"])
        path=self.evidence/"cancellation-target-create.json"; atomic(path,{k:cop[k] for k in ("id","platform_id","kind","status")}); self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"bind-resource","--state",str(self.evidence/"state.json"),"--role","cancellation_target","--operation",str(path)])
        cc=self.api("GET","/api/v1/managed-platforms/"+cancellation["id"]); cancel_intent=dict(intent,target_platform_id=cancellation["id"],expected_target_revision=cc["revision"],confirm_target_name=self.cancellation_target_spec["name"])
        review=self.api("POST","/api/v1/managed-platform-recovery/reviews",cancel_intent); op=self.api("POST","/api/v1/managed-platform-recovery/operations",dict(cancel_intent,review=review),"neon-cancel-"+self.run_id); operation_path="/api/v1/managed-platform-recovery-operations/"+op["id"]; self.wait_phase(operation_path,{"target-admitted","restoring-tenant.json","restoring-timeline.json","restoring-remote-storage.tar"}); self.api("POST",operation_path+"/cancel",{},expected=(202,)); self.wait(operation_path,300,("cancelled",))
        cancellation_receipt=self.api("GET","/api/v1/managed-platform-recovery-operations/"+op["id"]+"/native-cancellation-receipt"); observed=self.cancellation_receipt(cancellation_receipt,op["id"],artifact,receipt["manifest_sha256"],source["id"],sc["revision"],cancellation["id"],cc["revision"],cancellation_uid)
        self.pending_revocation={"run_id":self.run_id,"platform_id":source["id"],"namespace_uid":source_uid,"restore_operation_id":observed["operation_id"],"cancellation_target_namespace_uid":observed["namespace_uid"],**{key:value for key,value in observed.items() if key not in ("operation_id","namespace_uid")}}
    def namespace_uid(self,name):
        value=json.loads(self.k("get","namespace",name,"-o","json")); return value.get("metadata",{}).get("uid","")
    def cleanup(self):
        failed=[]
        for pid,name in reversed(self.platforms):
            try:
                current=self.namespace(pid); expected=self.bound.get(pid)
                if expected is None or current["metadata"]["uid"]!=expected[0] or current["metadata"].get("labels",{}).get("hakopod.io/owner-operation-id")!=expected[1]: raise RuntimeError("cleanup target identity changed")
                self.delete(pid,name)
            except Exception:failed.append(pid)
        for pid,_ in self.platforms:
            try:
                if self.k("get","namespace","managed-platform-"+pid,"--ignore-not-found","-o","name").strip():failed.append(pid)
            except Exception:failed.append(pid)
        if self.foreign is not None:
            namespace,uid,name=self.foreign
            try:
                current=self.namespace_uid(namespace)
                preserved=current==uid and json.loads(self.k("-n",namespace,"get","configmap",name,"-o","json")).get("data")=={"owner":"foreign"}
                if not preserved: raise RuntimeError("foreign sentinel did not survive platform teardown")
                if self.pending_revocation is not None:
                    self.pending_revocation["foreign_resources_preserved"]=True; self.record("revocation-cleanup",self.pending_revocation); self.pending_revocation=None
                if self.namespace_uid(namespace)!=uid: raise RuntimeError("foreign sentinel namespace identity changed")
                self.k("delete","namespace",namespace,"--wait=true",timeout=180)
                if self.k("get","namespace",namespace,"--ignore-not-found","-o","name").strip(): raise RuntimeError("foreign sentinel cleanup is incomplete")
                self.foreign=None
            except Exception: failed.append(namespace)
        try:self.command([sys.executable,self.a.control_plane_bridge,"cleanup","--kubeconfig",self.a.kubeconfig],240)
        except Exception:failed.append("control-plane-bridge")
        if failed: raise RuntimeError("owned cleanup is incomplete")

def parse(argv=None):
    p=argparse.ArgumentParser(description=__doc__)
    for name in ("source","kubeconfig","api-url","token-file","source-spec","target-spec","cancellation-target-spec","images","identities","destination-id","work-dir","gate-attestation","project","managed-tls-helper","runtime-spec-digest-helper","control-psql-command-file","control-plane-bridge","openssl","psql","proxy-password-file"):p.add_argument("--"+name,required=True)
    p.add_argument("--destination-revision",required=True,type=int); p.add_argument("--local-port",required=True,type=int); return p.parse_args(argv)

def main(argv=None):
    driver=None
    try:
        a=parse(argv); driver=Driver(a); driver.run(); driver.cleanup(); log=driver.evidence/"sanitized.log"; log.write_text("Neon native acceptance completed; secrets and raw output withheld.\n"); os.chmod(log,0o600)
        driver.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"finalize","--source",a.source,"--state",str(driver.evidence/"state.json"),"--events",str(driver.evidence/"events.json"),"--sanitized-log",str(log),"--report",str(driver.evidence/"report.json"),"--cleanup",str(driver.evidence/"cleanup.json")]); print(driver.evidence/"report.json"); return 0
    except Exception as error:
        if driver:
            try:driver.cleanup()
            except Exception:pass
        print("Neon native acceptance failed: "+str(error),file=sys.stderr); return 1
if __name__=="__main__": raise SystemExit(main())
