#!/usr/bin/env python3
"""Apply the MongoDB Community TLS resize correction to pinned upstream source.

This changes source in VM build scratch space. It does not build an image,
install a controller, or change a database.
"""
import argparse
from pathlib import Path
import subprocess


OPERATOR_REVISION = "75fa89bca8c1395a1beb0f244723f255f67f8719"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    args = parser.parse_args()
    root = args.source.resolve()
    revision = subprocess.check_output(
        ["git", "-C", str(root), "rev-parse", "HEAD"], text=True, timeout=10
    ).strip()
    if revision != OPERATOR_REVISION:
        raise ValueError("MongoDB operator upstream revision changed")
    if subprocess.check_output(
        ["git", "-C", str(root), "status", "--porcelain"], text=True, timeout=10
    ).strip():
        raise ValueError("Refusing to replace dirty MongoDB operator source")

    path = root / "mongodb-community-operator/controllers/replica_set_controller.go"
    old = "\tsts, err := r.client.GetStatefulSet(ctx, mdb.NamespacedName())\n\tif !statefulset.IsReady(sts, mdb.StatefulSetReplicasThisReconciliation()) && mdb.Spec.Security.TLS.Enabled {"
    new = """\tsts, err := r.client.GetStatefulSet(ctx, mdb.NamespacedName())
\t// During TLS scale-up, allocate the next pod before waiting for its agent.
\t// Once that pod is requested, the normal readiness path publishes its
\t// AutomationConfig before waiting for the new member to become ready.
\tif err == nil && mdb.Spec.Security.TLS.Enabled && scale.IsScalingUp(&mdb) && !scale.HasZeroReplicas(&mdb) && sts.Spec.Replicas != nil && int(*sts.Spec.Replicas) < mdb.StatefulSetReplicasThisReconciliation() {
\t\tr.log.Debug("Scaling up the TLS ReplicaSet, the next StatefulSet member must be created first")
\t\treturn false
\t}
\tif !statefulset.IsReady(sts, mdb.StatefulSetReplicasThisReconciliation()) && mdb.Spec.Security.TLS.Enabled {"""
    text = path.read_text()
    if text.count(old) != 1:
        raise ValueError("Pinned MongoDB TLS ordering context changed")
    target = root / "mongodb-community-operator/controllers/hakopod_tls_scaling_test.go"
    if target.exists():
        raise ValueError("Refusing to overwrite an existing MongoDB regression test")
    template = Path(__file__).resolve().parent.parent / "patches/mongodb-tls-scaling-test.go.txt"
    tests = template.read_text()
    path.write_text(text.replace(old, new))
    target.write_text(tests)
    print("Applied MongoDB TLS resize source correction; native acceptance is still required.")


if __name__ == "__main__":
    main()
