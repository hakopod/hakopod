#!/usr/bin/env python3
import importlib.util, pathlib, types, unittest

PATH=pathlib.Path(__file__).with_name("gateway-stream.py")
SPEC=importlib.util.spec_from_file_location("gateway_stream",PATH); GATEWAY=importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(GATEWAY)
UIDS=[f"{n:08x}-1111-1111-1111-{n:012x}" for n in range(1,10)]
IMAGE="example.invalid/envoy@sha256:"+"a"*64

def fixture(rs_uid=UIDS[2],pod_uid=UIDS[3],deployment_uid=UIDS[1]):
    spec={"runtimeClassName":"runsc","automountServiceAccountToken":False,"containers":[{"name":"api-gateway","image":IMAGE,"ports":[{"containerPort":8443}]}]}
    labels={"app.kubernetes.io/managed-by":"hakopod","hakopod.io/managed-platform-id":"a"*32,"app.kubernetes.io/component":"api-gateway"}
    deployment={"kind":"Deployment","metadata":{"name":"supabase-api-gateway","uid":deployment_uid,"generation":1,"labels":labels,"ownerReferences":[{"apiVersion":"v1","kind":"Namespace","name":"managed-platform-"+"a"*32,"uid":UIDS[0]}]},"spec":{"replicas":1,"template":{"spec":spec}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}
    rs={"kind":"ReplicaSet","metadata":{"name":"gateway-rs","uid":rs_uid,"ownerReferences":[{"kind":"Deployment","uid":deployment_uid,"controller":True}]},"spec":{"template":{"spec":spec}}}
    pod={"kind":"Pod","metadata":{"name":"gateway-pod","uid":pod_uid,"ownerReferences":[{"kind":"ReplicaSet","uid":rs_uid,"controller":True}]},"spec":spec,"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}
    return [deployment,rs,pod]

class GatewayTargetTest(unittest.TestCase):
    def setUp(self):
        self.args=types.SimpleNamespace(namespace="managed-platform-"+"a"*32,namespace_uid=UIDS[0],platform_id="a"*32,kubeconfig="/tmp/kubeconfig",context="k3d-hakopod-dev",image=IMAGE)
        self.items=fixture(); self.state={}
    def kube(self,*args):
        if args[:2]==("get","namespace"): return {"metadata":{"uid":UIDS[0],"labels":{"app.kubernetes.io/managed-by":"hakopod","hakopod.io/managed-platform-id":"a"*32}}}
        return {"items":self.items}
    def test_foreign_matching_label_resources_are_ignored(self):
        foreign_rs={"kind":"ReplicaSet","metadata":{"name":"foreign","uid":UIDS[5],"ownerReferences":[{"kind":"Deployment","uid":UIDS[6],"controller":True}]},"spec":{"template":{"spec":{}}}}
        foreign_pod={"kind":"Pod","metadata":{"name":"foreign","uid":UIDS[6],"ownerReferences":[{"kind":"ReplicaSet","uid":UIDS[5],"controller":True}]},"spec":{},"status":{}}
        self.items += [foreign_rs,foreign_pod]
        self.assertEqual(GATEWAY.resolve_target(self.args,self.kube,self.state)["pod_uid"],UIDS[3])
    def test_replaced_deployment_uid_is_refused(self):
        GATEWAY.resolve_target(self.args,self.kube,self.state)
        self.items=fixture(deployment_uid=UIDS[7])
        with self.assertRaises(RuntimeError): GATEWAY.resolve_target(self.args,self.kube,self.state)
    def test_completed_rollout_refreshes_replica_and_pod(self):
        first=GATEWAY.resolve_target(self.args,self.kube,self.state)
        self.items=fixture(rs_uid=UIDS[7],pod_uid=UIDS[8])
        second=GATEWAY.resolve_target(self.args,self.kube,self.state)
        self.assertNotEqual(first["owner_uid"],second["owner_uid"]); self.assertNotEqual(first["pod_uid"],second["pod_uid"])
    def test_rollout_unavailable_is_retryable(self):
        self.items[0]["status"]["updatedReplicas"]=0
        with self.assertRaises(GATEWAY.STREAM.TargetUnavailable): GATEWAY.resolve_target(self.args,self.kube,self.state)
        self.items=fixture(rs_uid=UIDS[7],pod_uid=UIDS[8])
        self.assertEqual(GATEWAY.resolve_target(self.args,self.kube,self.state)["pod_uid"],UIDS[8])

if __name__=="__main__": unittest.main()
