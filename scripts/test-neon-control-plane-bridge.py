#!/usr/bin/env python3
"""Pure ownership and network contract tests for the native Neon bridge."""
import copy
import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("neon_bridge", ROOT / "examples/neon-native-acceptance/control-plane-bridge.py")
BRIDGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BRIDGE)


class Tests(unittest.TestCase):
    def scheduling_fixture(self, directory):
        root = Path(directory)
        uids = {"node-"+str(i):str(i)*8+"-2222-3333-4444-555555555555" for i in range(1,4)}
        gate = {"schema_version":1,"kind":"neon","environment":"development","context":"k3d-hakopod-dev","kubeconfig":str(root/"kubeconfig"),"cluster_uid":"aaaaaaaa-2222-3333-4444-555555555555","node_uids":uids,"expires_at":(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=1)).isoformat()}
        gate_path = root/"gate.json"; gate_path.write_text(json.dumps(gate)); gate_path.chmod(0o600)
        runtime = {"name":"runsc","uid":"bbbbbbbb-2222-3333-4444-555555555555","handler":"runsc","pod_fixed":{"cpu":"20m","memory":"50Mi"},"node_selector":{}}
        policy = {"schema_version":1,"gate_sha256":hashlib.sha256(gate_path.read_bytes()).hexdigest(),"scheduling_pool":"native-final","runtime_class":runtime}
        policy_path = root/"policy.json"; policy_path.write_text(json.dumps(policy)); policy_path.chmod(0o600)
        cluster = {"metadata":{"uid":gate["cluster_uid"]}}
        nodes = {"items":[{"metadata":{"name":name,"uid":uid,"labels":{BRIDGE.POOL_KEY:"native-final"}},"spec":{"taints":[{"key":BRIDGE.POOL_KEY,"value":"native-final","effect":"NoSchedule"}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}} for name,uid in uids.items()]}
        observed = {"metadata":{"uid":runtime["uid"]},"handler":"runsc","overhead":{"podFixed":runtime["pod_fixed"]}}
        return policy_path,gate_path,gate,policy,[cluster,nodes,observed]

    def expected_scheduling(self):
        return {"nodeSelector":{BRIDGE.POOL_KEY:"native-final"},"tolerations":[{"key":BRIDGE.POOL_KEY,"operator":"Equal","value":"native-final","effect":"NoSchedule"}],"runtimeClassName":"runsc","affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchFields":[{"key":"metadata.name","operator":"In","values":[name]}]} for name in ["node-1","node-2","node-3"]]}}}}

    def test_empty_policy_preserves_legacy_bridge(self):
        with mock.patch.object(BRIDGE,"kube") as kube, mock.patch.object(BRIDGE,"protected",return_value=b"fixture"):
            self.assertEqual(BRIDGE.scheduling_policy("","",""),{})
            pod=next(item for item in json.loads(BRIDGE.manifest(["a"*32]))["items"] if item["kind"]=="Deployment")["spec"]["template"]["spec"]
            self.assertFalse({"nodeSelector","tolerations","runtimeClassName"}&set(pod))
            kube.assert_not_called()

    def test_policy_binds_live_pool_runtime_uid_and_overhead(self):
        with tempfile.TemporaryDirectory() as directory:
            path,gate,identity,policy,responses=self.scheduling_fixture(directory)
            with mock.patch.object(BRIDGE,"kube",side_effect=[json.dumps(item) for item in responses]):
                self.assertEqual(BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"]),self.expected_scheduling())
            policy["runtime_class"]=None; path.write_text(json.dumps(policy))
            with mock.patch.object(BRIDGE,"kube",side_effect=[json.dumps(item) for item in responses[:2]]):
                actual=BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])
                self.assertEqual(actual,{key:value for key,value in self.expected_scheduling().items() if key!="runtimeClassName"})

    def test_policy_rejects_changed_gate_or_unprotected_input(self):
        with tempfile.TemporaryDirectory() as directory:
            path,gate,identity,_,_=self.scheduling_fixture(directory)
            with mock.patch.object(BRIDGE,"kube") as kube:
                gate.write_text(gate.read_text()+"\n")
                with self.assertRaisesRegex(RuntimeError,"binding"): BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])
                path.chmod(0o644)
                with self.assertRaisesRegex(RuntimeError,"protected"): BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])
                symbolic=Path(directory)/"symbolic"; symbolic.symlink_to(gate)
                with self.assertRaises(OSError): BRIDGE.protected_policy_input(str(symbolic))
                kube.assert_not_called()

    def test_runtime_selector_is_bound_to_every_attested_node_without_implicit_tolerations(self):
        with tempfile.TemporaryDirectory() as directory:
            path,gate,identity,policy,responses=self.scheduling_fixture(directory)
            selector={"hakopod.com.node-restriction.kubernetes.io/default-runtime":"runsc"}
            policy["runtime_class"]["node_selector"]=selector; policy["runtime_class"]["pod_fixed"]={}; path.write_text(json.dumps(policy))
            responses[2]["scheduling"]={"nodeSelector":selector}; responses[2].pop("overhead")
            for node in responses[1]["items"]: node["metadata"]["labels"].update(selector)
            with mock.patch.object(BRIDGE,"kube",side_effect=[json.dumps(item) for item in responses]):
                actual=BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])
                self.assertEqual(actual["nodeSelector"],{BRIDGE.POOL_KEY:"native-final",**selector})
                self.assertEqual(actual["tolerations"],self.expected_scheduling()["tolerations"])
            responses[1]["items"][1]["metadata"]["labels"].pop(next(iter(selector)))
            with mock.patch.object(BRIDGE,"kube",side_effect=[json.dumps(item) for item in responses]):
                with self.assertRaisesRegex(RuntimeError,"runtime selector"): BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])

    def test_policy_refuses_changed_cluster_pool_or_runtime(self):
        for mutation in ("cluster","node","pool","taint","other-taint","ready","runtime-uid","handler","overhead","runtime-scheduling"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                path,gate,identity,_,responses=self.scheduling_fixture(directory)
                if mutation=="cluster": responses[0]["metadata"]["uid"]="replaced"
                elif mutation=="node": responses[1]["items"][0]["metadata"]["uid"]="replaced"
                elif mutation=="pool": responses[1]["items"][0]["metadata"]["labels"][BRIDGE.POOL_KEY]="foreign"
                elif mutation=="taint": responses[1]["items"][0]["spec"]["taints"][0]["effect"]="PreferNoSchedule"
                elif mutation=="other-taint": responses[1]["items"][0]["spec"]["taints"].append({"key":"foreign","effect":"NoExecute"})
                elif mutation=="ready": responses[1]["items"][0]["status"]["conditions"][0]["status"]="False"
                elif mutation=="runtime-uid": responses[2]["metadata"]["uid"]="replaced"
                elif mutation=="handler": responses[2]["handler"]="runc"
                elif mutation=="overhead": responses[2]["overhead"]["podFixed"]={"cpu":"30m","memory":"50Mi"}
                else: responses[2]["scheduling"]={"tolerations":[{"operator":"Exists"}]}
                with mock.patch.object(BRIDGE,"kube",side_effect=[json.dumps(item) for item in responses]):
                    with self.assertRaises(RuntimeError): BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])

    def test_policy_refuses_expired_gate_without_cluster_access(self):
        with tempfile.TemporaryDirectory() as directory:
            path,gate,identity,policy,_=self.scheduling_fixture(directory)
            identity["expires_at"]="2000-01-01T00:00:00Z"; gate.write_text(json.dumps(identity)); policy["gate_sha256"]=hashlib.sha256(gate.read_bytes()).hexdigest(); path.write_text(json.dumps(policy))
            with mock.patch.object(BRIDGE,"kube") as kube:
                with self.assertRaisesRegex(RuntimeError,"expired"): BRIDGE.scheduling_policy(str(path),str(gate),identity["kubeconfig"])
                kube.assert_not_called()

    def test_both_helper_workloads_get_only_explicit_scheduling(self):
        scheduling=self.expected_scheduling()
        with mock.patch.object(BRIDGE,"protected",return_value=b"fixture"):
            pod=next(item for item in json.loads(BRIDGE.manifest(["a"*32],scheduling))["items"] if item["kind"]=="Deployment")["spec"]["template"]["spec"]
        self.assertEqual({key:pod[key] for key in scheduling},scheduling)
        platform,secret,_,source=self.proxy()
        source["spec"]["template"]["spec"]["nodeSelector"]={BRIDGE.POOL_KEY:"untrusted-source"}
        for ca_kind in ("correct","wrong"):
            created=[]
            def kube(_config,*args,data=None):
                if "rollout" in args: return ""
                if "neon-proxy" in args: return json.dumps(source)
                raise AssertionError(args)
            def install(_config,_namespace,_state,_value,objects,_resources): created.extend(objects)
            with tempfile.TemporaryDirectory() as directory, mock.patch.object(BRIDGE,"ROOT",Path(directory)), mock.patch.object(BRIDGE,"kube",side_effect=kube), mock.patch.object(BRIDGE,"install_owned",side_effect=install), mock.patch.object(BRIDGE,"protected",return_value=b"fixture"):
                BRIDGE.probe_start("/kubeconfig",platform,secret,ca_kind,scheduling)
            actual=next(item for item in created if item["kind"]=="Deployment")["spec"]["template"]["spec"]
            self.assertEqual({key:actual[key] for key in scheduling},scheduling)
            self.assertNotIn("nodeName",actual)

    def test_cleanup_does_not_require_live_scheduling_authority(self):
        with mock.patch.object(BRIDGE.sys,"argv",["bridge","cleanup","--kubeconfig","/kubeconfig","--scheduling-policy","/expired","--gate-attestation","/expired-gate"]), mock.patch.object(BRIDGE,"scheduling_policy") as policy, mock.patch.object(BRIDGE,"cleanup") as cleanup:
            BRIDGE.main(); policy.assert_not_called(); cleanup.assert_called_once_with("/kubeconfig")

    def test_ingress_scopes_proxy_and_controller_to_owned_platforms(self):
        platforms = ["a" * 32, "b" * 32]
        with mock.patch.object(BRIDGE, "protected", return_value=b"fixture"):
            manifest = json.loads(BRIDGE.manifest(platforms))
        policy = next(item for item in manifest["items"] if item["metadata"]["name"] == "hakopod-neon-control-ingress")
        rules = policy["spec"]["ingress"]
        self.assertEqual(len(rules), 1)
        self.assertEqual(rules[0]["ports"], [{"protocol": "TCP", "port": 443}])
        self.assertEqual(rules[0]["from"], [{"namespaceSelector": {"matchExpressions": [{"key": "hakopod.io/managed-platform-id", "operator": "In", "values": platforms}]}, "podSelector": {"matchExpressions": [{"key": "hakopod.io/neon-role", "operator": "In", "values": ["proxy", "storage-controller"]}]}}])

    def proxy(self):
        platform = "a" * 32
        secret = "platform-tls-proxy-" + "b" * 16 + "-r1"
        image = "registry.example/current-neon-proxy@sha256:" + "c" * 64
        deployment = {"metadata": {"name": "neon-proxy", "namespace": "managed-platform-" + platform, "uid": "11111111-2222-3333-4444-555555555555", "labels": {"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platform}}, "spec": {"template": {"metadata": {"labels": {"hakopod.io/managed-platform-id": platform, "hakopod.io/neon-role": "proxy"}}, "spec": {"containers": [{"name": "proxy", "image": image}], "volumes": [{"name": "proxy-auth", "secret": {"secretName": secret}}]}}}}
        return platform, secret, image, deployment

    def test_issuer_probe_uses_current_pinned_owned_proxy_image(self):
        platform, secret, image, deployment = self.proxy()
        self.assertEqual(BRIDGE.owned_proxy_image(deployment, platform, secret), image)

    def test_issuer_probe_refuses_foreign_or_deleting_deployment(self):
        platform, secret, _, deployment = self.proxy()
        for field, value in [("namespace", "managed-platform-foreign"), ("uid", "invalid"), ("deletionTimestamp", "now")]:
            changed = copy.deepcopy(deployment)
            changed["metadata"][field] = value
            with self.assertRaisesRegex(RuntimeError, "ownership"):
                BRIDGE.owned_proxy_image(changed, platform, secret)
        changed = copy.deepcopy(deployment)
        changed["spec"]["template"]["metadata"]["labels"]["hakopod.io/managed-platform-id"] = "d" * 32
        with self.assertRaisesRegex(RuntimeError, "ownership"):
            BRIDGE.owned_proxy_image(changed, platform, secret)

    def test_issuer_probe_refuses_mutable_image_or_foreign_tls_snapshot(self):
        platform, secret, _, deployment = self.proxy()
        changed = copy.deepcopy(deployment)
        changed["spec"]["template"]["spec"]["containers"][0]["image"] = "registry.example/proxy:latest"
        with self.assertRaisesRegex(RuntimeError, "image or TLS"):
            BRIDGE.owned_proxy_image(changed, platform, secret)
        with self.assertRaisesRegex(RuntimeError, "image or TLS"):
            BRIDGE.owned_proxy_image(deployment, platform, "foreign-secret")

    def test_admission_preflight_checks_deployments_and_their_pods(self):
        with mock.patch.object(BRIDGE, "protected", return_value=b"development fixture"):
            objects = json.loads(BRIDGE.manifest(["a"*32], self.expected_scheduling()))["items"]
        with mock.patch.object(BRIDGE, "kube") as kube:
            BRIDGE.validate_manifests("/kubeconfig", objects)
        args, kwargs = kube.call_args
        self.assertIn("--dry-run=server", args)
        candidates = json.loads(kwargs["data"])["items"]
        self.assertEqual(len(candidates), len(objects)+1)
        deployment = next(item for item in objects if item["kind"] == "Deployment")
        pod = next(item for item in candidates if item["kind"] == "Pod")
        self.assertEqual(pod["spec"], deployment["spec"]["template"]["spec"])
        for term in pod["spec"]["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]["nodeSelectorTerms"]:
            self.assertEqual(len(term["matchFields"][0]["values"]), 1)

    def ownership_fixture(self, directory, fail=None):
        # Explicit development API fixture; no invented real-cluster evidence.
        namespace = "fixture"
        ns_uid = "aaaaaaaa-2222-3333-4444-555555555555"
        objects = [{"apiVersion":"v1", "kind":"ConfigMap", "metadata":{"name":name,"namespace":namespace}, "data":{"fixture":"development"}} for name in ["one", "two"]]
        resources = ("configmap/one", "configmap/two")
        state = Path(directory)/"state.json"
        live, deleted, calls = {}, [], []
        def kube(_config, *args, data=None):
            calls.append(args)
            if "--dry-run=server" in args:
                if fail == "admission": raise RuntimeError("fixture admission failure")
                return ""
            if args[:2] == ("get", "namespace"): return json.dumps({"metadata":{"uid":ns_uid}})
            if args[:3] == ("-n", namespace, "get"): return json.dumps(live[args[3]]) if args[3] in live else ""
            if args[:3] == ("create", "-f", "-"):
                item = json.loads(data); name = item["metadata"]["name"]
                journal = json.loads(state.read_text())
                self.assertEqual(item["metadata"]["annotations"][BRIDGE.INSTALL_KEY], journal["install_id"])
                self.assertEqual(state.stat().st_mode & 0o777, 0o600)
                item["metadata"]["uid"] = ("1" if name == "one" else "2")*8+"-2222-3333-4444-555555555555"
                if name == "two" and fail == "rejected": raise RuntimeError("fixture create rejected")
                live["configmap/"+name] = item
                if name == "two" and fail == "response-lost": raise RuntimeError("fixture response lost")
                return json.dumps(item)
            if args[:2] == ("delete", "--raw"):
                name = args[2].split("/")[-1]; key = "configmap/"+name
                options = json.loads(data)
                self.assertEqual(options["preconditions"], {"uid":live[key]["metadata"]["uid"]})
                deleted.append(key); del live[key]; return "{}"
            raise AssertionError(args)
        return namespace, objects, resources, state, live, deleted, calls, kube

    def test_admission_failure_has_no_create_or_ownership_state(self):
        with tempfile.TemporaryDirectory() as directory:
            namespace, objects, resources, state, live, deleted, calls, kube = self.ownership_fixture(directory, "admission")
            with mock.patch.object(BRIDGE, "kube", side_effect=kube), self.assertRaisesRegex(RuntimeError, "admission"):
                BRIDGE.install_owned("/kubeconfig", namespace, state, {}, objects, resources)
            self.assertFalse(state.exists()); self.assertFalse(live); self.assertFalse(deleted)
            self.assertFalse(any(args[:3] == ("create", "-f", "-") for args in calls))

    def test_partial_create_and_lost_response_cleanup_use_persisted_ownership(self):
        for failure in ("rejected", "response-lost"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                namespace, objects, resources, state, live, deleted, _, kube = self.ownership_fixture(directory, failure)
                with mock.patch.object(BRIDGE, "kube", side_effect=kube), mock.patch.object(BRIDGE, "ROOT", Path(directory)):
                    with self.assertRaisesRegex(RuntimeError, "helper create failed"):
                        BRIDGE.install_owned("/kubeconfig", namespace, state, {}, objects, resources)
                    journal = BRIDGE.read_state(state, set(), resources)
                    self.assertEqual(set(journal["uids"]), {"configmap/one"})
                    BRIDGE.cleanup_owned("/kubeconfig", namespace, state, journal, resources)
                self.assertEqual(len(deleted), 1 if failure == "rejected" else 2)
                self.assertFalse(live); self.assertFalse(state.exists())

    def test_cleanup_refuses_foreign_marker_changed_uid_or_namespace(self):
        for changed in ("marker", "uid", "namespace"):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as directory:
                namespace, objects, resources, state, live, deleted, _, kube = self.ownership_fixture(directory)
                with mock.patch.object(BRIDGE, "kube", side_effect=kube):
                    journal = BRIDGE.install_owned("/kubeconfig", namespace, state, {}, objects, resources)
                    if changed == "marker": live["configmap/two"]["metadata"]["annotations"][BRIDGE.INSTALL_KEY] = "b"*32
                    elif changed == "uid": live["configmap/two"]["metadata"]["uid"] = "bbbbbbbb-2222-3333-4444-555555555555"
                    else: journal["namespace_uid"] = "bbbbbbbb-2222-3333-4444-555555555555"
                    with self.assertRaisesRegex(RuntimeError, "foreign|namespace"):
                        BRIDGE.cleanup_owned("/kubeconfig", namespace, state, journal, resources)
                self.assertFalse(deleted); self.assertTrue(state.exists())

    def test_cleanup_retry_accepts_an_already_absent_owned_resource(self):
        with tempfile.TemporaryDirectory() as directory:
            namespace, objects, resources, state, live, deleted, _, kube = self.ownership_fixture(directory)
            with mock.patch.object(BRIDGE, "kube", side_effect=kube):
                journal = BRIDGE.install_owned("/kubeconfig", namespace, state, {}, objects, resources)
                del live["configmap/one"]
                BRIDGE.cleanup_owned("/kubeconfig", namespace, state, journal, resources)
            self.assertEqual(deleted, ["configmap/two"]); self.assertFalse(state.exists())

    def test_allowlist_patch_conditions_reject_replaced_policy(self):
        for drift in (None, "replacement", "spec"):
            with self.subTest(drift=drift), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); (root/"allowed-platforms.json").write_text("fixture")
                uid = "11111111-2222-3333-4444-555555555555"
                prior = {"platform_ids":["a"*32], "install_id":"b"*32, "namespace_uid":uid, "uids":{name:uid for name in BRIDGE.BRIDGE_RESOURCES}}
                owned = {"metadata":{"uid":uid,"resourceVersion":"17","annotations":{BRIDGE.INSTALL_KEY:prior["install_id"]}}}
                patches = []
                def kube(_config, *args, data=None):
                    if "patch" in args:
                        self.assertIn("--type=json", args)
                        patch = json.loads(args[args.index("-p")+1]); patches.append(patch)
                        self.assertEqual(patch[:2], [{"op":"test","path":"/metadata/uid","value":uid},{"op":"test","path":"/metadata/resourceVersion","value":"17"}])
                        if drift == "replacement": raise RuntimeError("fixture JSON Patch UID test failed")
                        owned["spec"] = {} if drift == "spec" else patch[2]["value"]
                        return "{}"
                    if "rollout" in args: return ""
                    return json.dumps(owned)
                with mock.patch.object(BRIDGE,"ROOT",root), mock.patch.object(BRIDGE,"read_state",return_value=prior), mock.patch.object(BRIDGE,"recover_owned",return_value=prior["uids"]), mock.patch.object(BRIDGE,"observe",return_value=owned), mock.patch.object(BRIDGE,"protected",return_value=b"fixture"), mock.patch.object(BRIDGE,"kube",side_effect=kube), mock.patch.object(BRIDGE,"write_state") as write:
                    if drift:
                        with self.assertRaisesRegex(RuntimeError,"UID test failed|specification changed"): BRIDGE.allow("/kubeconfig","c"*32)
                        write.assert_not_called()
                        self.assertEqual(prior["platform_ids"],["a"*32])
                    else:
                        BRIDGE.allow("/kubeconfig","c"*32)
                        self.assertEqual(prior["platform_ids"],["a"*32,"c"*32])
                self.assertEqual(len(patches),1)


if __name__ == "__main__":
    unittest.main()
