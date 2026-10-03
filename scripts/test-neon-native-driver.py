#!/usr/bin/env python3
"""Pure contract tests for the destructive Neon native driver."""
import importlib.util, io, json, os, tempfile, unittest, urllib.error
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
TEST_TMP = Path(os.environ.get("TMPDIR", "/tmp"))
spec = importlib.util.spec_from_file_location("neon_driver", ROOT / "examples/neon-native-acceptance/driver.py")
DRIVER = importlib.util.module_from_spec(spec); spec.loader.exec_module(DRIVER)

def neon(name, prefix):
    return {"schema_version":1,"name":name,"kind":"neon","neon":{"safekeepers":3,"pageservers":2,"compute_replicas":1,"object_storage_prefix":prefix}}

class Args:
    api_url="http://127.0.0.1:8800"
    destination_id="c"*32; destination_revision=1

class Tests(unittest.TestCase):
    def fixture(self, directory):
        base=Path(directory); args=Args()
        work_parent=Path("/srv/hakopod-backup-scratch") if str(TEST_TMP).startswith("/srv/hakopod-backup-scratch/") else Path("/tmp")
        args.work_dir=str(work_parent/("hakopod-neon-native-"+base.name))
        images={name:"registry.example/neon-"+name+"@sha256:"+"d"*64 for name in DRIVER.COMPONENTS}
        identities={name:{"uid":10001,"gid":10001,"image":image} for name,image in images.items()}
        for field,value in (("source_spec",neon("source","source-prefix")),("target_spec",neon("target","target-prefix")),("cancellation_target_spec",neon("cancel-target","cancel-prefix")),("images",images),("identities",identities)):
            path=base/(field+".json"); path.write_text(json.dumps(value)); setattr(args,field,str(path))
        return args
    @mock.patch.object(DRIVER,"validate_attestation",return_value={})
    @mock.patch.object(DRIVER.sys,"platform","linux")
    @mock.patch.object(DRIVER.os,"uname",return_value=mock.Mock(machine="x86_64"))
    def test_exact_contract(self,_uname,_attestation):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            args=self.fixture(directory)
            source,target,cancellation=DRIVER.validate_contract(args); self.assertEqual((source["name"],target["name"],cancellation["name"]),("source","target","cancel-target"))
    @mock.patch.object(DRIVER,"validate_attestation",return_value={})
    @mock.patch.object(DRIVER.sys,"platform","linux")
    @mock.patch.object(DRIVER.os,"uname",return_value=mock.Mock(machine="x86_64"))
    def test_rejects_shared_restore_prefix(self,_uname,_attestation):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            args=self.fixture(directory); target=DRIVER.load(args.target_spec); target["neon"]["object_storage_prefix"]="source-prefix"; Path(args.target_spec).write_text(json.dumps(target))
            with self.assertRaisesRegex(RuntimeError,"not separate"): DRIVER.validate_contract(args)
    @mock.patch.object(DRIVER,"validate_attestation",return_value={})
    @mock.patch.object(DRIVER.sys,"platform","linux")
    @mock.patch.object(DRIVER.os,"uname",return_value=mock.Mock(machine="x86_64"))
    def test_rejects_reused_cancellation_target(self,_uname,_attestation):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            args=self.fixture(directory); cancellation=DRIVER.load(args.cancellation_target_spec); cancellation["neon"]["object_storage_prefix"]="target-prefix"; Path(args.cancellation_target_spec).write_text(json.dumps(cancellation))
            with self.assertRaisesRegex(RuntimeError,"not separate"): DRIVER.validate_contract(args)
    @mock.patch.object(DRIVER,"validate_attestation",return_value={})
    @mock.patch.object(DRIVER.sys,"platform","linux")
    @mock.patch.object(DRIVER.os,"uname",return_value=mock.Mock(machine="x86_64"))
    def test_rejects_mutable_image(self,_uname,_attestation):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            args=self.fixture(directory); images=DRIVER.load(args.images); images["proxy"]="registry.example/proxy:latest"; Path(args.images).write_text(json.dumps(images))
            with self.assertRaisesRegex(RuntimeError,"pinned"): DRIVER.validate_contract(args)
    @mock.patch.object(DRIVER,"validate_attestation",return_value={})
    @mock.patch.object(DRIVER.sys,"platform","linux")
    @mock.patch.object(DRIVER.os,"uname",return_value=mock.Mock(machine="x86_64"))
    def test_rejects_unbounded_work_path(self,_uname,_attestation):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            args=self.fixture(directory); args.work_dir="/var/tmp/hakopod-neon-native-test"
            with self.assertRaisesRegex(RuntimeError,"bounded"): DRIVER.validate_contract(args)
    def test_atomic_refuses_overwrite(self):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            path=Path(directory)/"event.json"; DRIVER.atomic(path,{"ok":True})
            with self.assertRaisesRegex(RuntimeError,"not fresh"): DRIVER.atomic(path,{"ok":False})
    def test_protected_text_rejects_symbolic_and_oversized_inputs(self):
        with tempfile.TemporaryDirectory(dir=TEST_TMP) as directory:
            root=Path(directory); token=root/"token"; token.write_text("protected-token-value"); token.chmod(0o600)
            self.assertEqual(DRIVER.protected_text(token),"protected-token-value")
            symbolic=root/"symbolic"; symbolic.symlink_to(token)
            with self.assertRaisesRegex(RuntimeError,"symbolic"): DRIVER.protected_text(symbolic)
            token.unlink(); token.write_bytes(b"x"*4097); token.chmod(0o600)
            with self.assertRaisesRegex(RuntimeError,"unbounded"): DRIVER.protected_text(token)
    def test_command_rejects_unbounded_input_and_output(self):
        driver=self.bare_driver()
        with self.assertRaisesRegex(RuntimeError,"input"):
            driver.command(["true"],data=b"x"*(DRIVER.MAX_COMMAND_INPUT+1))
        with self.assertRaisesRegex(RuntimeError,"output withheld"):
            driver.command([os.environ.get("PYTHON","python3"),"-c","import os; os.write(1,b'x'*(3<<20))"])
    def test_command_rejects_timeout_and_malformed_argv(self):
        driver=self.bare_driver()
        with self.assertRaisesRegex(RuntimeError,"timeout"):
            driver.command(["true"],timeout=0)
        with self.assertRaisesRegex(RuntimeError,"malformed"):
            driver.command(["true","bad\x00argument"])
    def bare_driver(self):
        driver=DRIVER.Driver.__new__(DRIVER.Driver); driver.a=mock.Mock(api_url="http://127.0.0.1:8800"); driver.token="protected-token-value"; return driver
    def test_runtime_uses_neon_roles_and_numbered_services(self):
        driver=self.bare_driver(); driver.run_id="a"*32; driver.source_spec=neon("source","source-prefix")
        roles=("storage-controller","pageserver","safekeeper","compute","proxy")
        pods=[]
        for role in roles:
            containers=[{"name":role}]
            if role=="compute": containers.append({"name":"compute-tls"})
            pods.append({"metadata":{"labels":{"hakopod.io/neon-role":role}},"spec":{"containers":containers}})
        driver.pods=mock.Mock(return_value=("managed-platform-id",pods))
        service_names=("neon-storage-controller","neon-proxy","neon-pageserver-0","neon-pageserver-1","neon-safekeeper-0","neon-safekeeper-1","neon-safekeeper-2","neon-compute-0","neon-compute-0-control")
        driver.k=mock.Mock(return_value=json.dumps({"items":[{"metadata":{"name":name}} for name in service_names]}))
        observation={"tenant_id":"b"*32,"timeline_id":"c"*32,"tenant_generation":1,"timeline_generation":1,"compute_ids":["d"*32],"tenant_created":True,"timeline_created":True,"compute_started":True,"compute_stopped":True,"timeline_deleted":True,"tenant_deleted":True,"tls":{"client_verification_enforced":True,"server_verified":True,"plaintext_refused":True,"services":list(roles)}}
        driver.native={"e"*32:dict(observation,compute_names=["compute-0"])}
        driver.api=mock.Mock(return_value={"observation":observation})
        tls,lifecycle=driver.runtime("e"*32,"namespace-uid")
        self.assertTrue(tls["plaintext_refused"]); self.assertEqual(lifecycle["compute_names"],["compute-0"])
    def test_runtime_rejects_absent_native_probe_evidence(self):
        driver=self.bare_driver(); driver.run_id="a"*32; driver.source_spec=neon("source","source-prefix"); driver.native={}
        driver.pods=mock.Mock(return_value=("managed-platform-id",[]))
        with self.assertRaises(RuntimeError): driver.runtime("e"*32,"namespace-uid")
    def test_ownership_requests_read_only_native_probe(self):
        driver=self.bare_driver(); driver.run_id="a"*32; driver.native={}; driver.foreign=None
        pid="e"*32; opid="f"*32
        driver.namespace=mock.Mock(return_value={"metadata":{"uid":"namespace-uid","labels":{"hakopod.io/owner-operation-id":opid}}})
        driver.namespace_uid=mock.Mock(return_value="sentinel-uid")
        driver.k=mock.Mock(side_effect=["1"*32+"\n"+"2"*32+"\n"+"3"*32,json.dumps({"data":{"owner":"foreign"}})])
        driver.command=mock.Mock(return_value="")
        current={"revision":1}; probe={"platform_id":pid,"platform_revision":1,"namespace_uid":"namespace-uid","ownership_capability_verified":True,"wrong_owner_refused":True,"wrong_deletion_token_refused":True}
        driver.api=mock.Mock(side_effect=[current,probe])
        result=driver.ownership(pid,opid)
        self.assertTrue(result["foreign_owner_refused"])
        self.assertEqual(driver.api.call_args_list[1].args,("POST","/api/v1/managed-platforms/"+pid+"/native-probe",{"expected_revision":1}))
    @mock.patch.object(DRIVER.ssl,"create_default_context")
    @mock.patch.object(DRIVER.socket,"create_connection")
    @mock.patch.object(DRIVER.subprocess,"Popen")
    def test_proxy_auth_binds_platform_endpoint_in_startup_options(self,popen,create_connection,create_context):
        platform_id="e"*32; raw=mock.MagicMock(); raw.recv.return_value=b"S"; create_connection.return_value=raw
        connection=mock.MagicMock(); connection.__enter__.return_value=connection; connection.recv.return_value=b"R"; create_context.return_value.wrap_socket.return_value=connection
        process=mock.MagicMock(); process.poll.return_value=None; popen.return_value=process
        driver=self.bare_driver(); driver.a.kubeconfig="/protected/kubeconfig"
        self.assertEqual(driver.proxy_auth_message("managed-platform-"+platform_id,"deployment/proxy",15433,Path("/protected/ca.crt"),platform_id),b"R")
        payload=connection.sendall.call_args.args[0]
        self.assertEqual(payload[8:],b"user\x00cloud_admin\x00database\x00postgres\x00options\x00endpoint="+platform_id.encode()+b"\x00\x00")
    def test_recovery_receipt_requires_exact_operation_artifact_and_scope_binding(self):
        driver=self.bare_driver(); source,target="1"*32,"2"*32; operation,artifact="3"*32,"4"*32
        value={"operation_id":operation,"status":"succeeded","artifact_id":artifact,"manifest_sha256":"5"*64,"source_platform_id":source,"source_revision":7,"source_namespace_uid":"source-uid","target_platform_id":target,"target_revision":9,"format":"hakopod-neon-recovery-v1","parts":["tenant.json","timeline.json","remote-storage.tar"],"neon":{"tenant_id":"6"*32}}
        self.assertEqual(driver.recovery_receipt(value,operation,artifact,source,7,"source-uid",target,9),value["neon"])
        for field,replacement in (("operation_id","7"*32),("artifact_id","8"*32),("source_namespace_uid","wrong"),("target_revision",10)):
            changed=dict(value); changed[field]=replacement
            with self.assertRaisesRegex(RuntimeError,"receipt binding"): driver.recovery_receipt(changed,operation,artifact,source,7,"source-uid",target,9)
    def test_cancellation_receipt_requires_exact_terminal_binding(self):
        driver=self.bare_driver(); driver.a.project="native-neon"; driver.cancellation_target_spec=neon("cancel","cancel-prefix"); source,target="1"*32,"2"*32; operation,artifact="3"*32,"4"*32
        value={"schema_version":1,"operation_id":operation,"project":"native-neon","environment":"development","source_platform_id":source,"source_revision":7,"target_platform_id":target,"target_revision":9,"artifact_id":artifact,"manifest_sha256":"5"*64,"tenant_id":"6"*32,"timeline_id":"7"*32,"tenant_generation":1,"timeline_generation":2,"journal_entries":2,"journal_phase_counts":{"complete":2,"empty_complete":0,"untouched_complete":0},"cleanup_pending":False,"operation_authority_refused":True,"namespace_uid":"target-uid","deployment_count":1,"statefulset_count":6,"workload_replicas_zero":True,"pods_absent":True,"staging_prefix_empty":True}
        self.assertIs(driver.cancellation_receipt(value,operation,artifact,"5"*64,source,7,target,9,"target-uid"),value)
        for field,replacement in (("operation_id","8"*32),("manifest_sha256","9"*64),("namespace_uid","wrong"),("pods_absent",False)):
            changed=dict(value); changed[field]=replacement
            with self.assertRaisesRegex(RuntimeError,"cancellation receipt binding"): driver.cancellation_receipt(changed,operation,artifact,"5"*64,source,7,target,9,"target-uid")
    def test_wait_phase_refuses_terminal_operation_before_cleanup_journal(self):
        driver=self.bare_driver(); driver.api=mock.Mock(return_value={"status":"cancelled","phase":"cancelled"})
        with self.assertRaisesRegex(RuntimeError,"before the required phase"): driver.wait_phase("/api/v1/managed-platform-recovery-operations/"+"1"*32,{"target-admitted"},1)
    @mock.patch.object(DRIVER.HTTP,"open")
    def test_api_binds_exact_path_and_headers(self,open_request):
        response=mock.MagicMock(); response.__enter__.return_value=response; response.status=200; response.read.return_value=b'{"ok":true}'
        response.geturl.return_value="http://127.0.0.1:8800/api/v1/check"; open_request.return_value=response
        self.assertEqual(self.bare_driver().api("POST","/api/v1/check",{"a":1},"fixed-key"),{"ok":True})
        request=open_request.call_args.args[0]; self.assertEqual(request.full_url,"http://127.0.0.1:8800/api/v1/check"); self.assertEqual(request.get_header("Idempotency-key"),"fixed-key"); self.assertEqual(request.data,b'{"a":1}')
    @mock.patch.object(DRIVER.HTTP,"open")
    def test_api_rejects_redirect(self,open_request):
        open_request.side_effect=urllib.error.HTTPError("http://127.0.0.1:8800/api/v1/x",302,"redirect",{},io.BytesIO(b'{}'))
        with self.assertRaisesRegex(RuntimeError,"unexpected"): self.bare_driver().api("GET","/api/v1/x")
    @mock.patch.object(DRIVER.HTTP,"open")
    def test_api_rejects_oversized_body(self,open_request):
        response=mock.MagicMock(); response.__enter__.return_value=response; response.status=200; response.read.return_value=b'x'*((1<<20)+1)
        response.geturl.return_value="http://127.0.0.1:8800/api/v1/x"; open_request.return_value=response
        with self.assertRaisesRegex(RuntimeError,"unexpected"): self.bare_driver().api("GET","/api/v1/x")

if __name__ == "__main__": unittest.main()
