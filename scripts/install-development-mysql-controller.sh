#!/usr/bin/env bash
# Install only into the named development cluster. This is not a production installer.
set -euo pipefail
: "${HAKOPOD_TEST_KUBECONFIG:?Set the named development kubeconfig}"
context=k3d-hakopod-dev
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" config get-contexts -o name | grep -qx "$context"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
base=https://raw.githubusercontent.com/mysql/mysql-operator/fd5c6bcf4bc3778dc4cb324c69053cac58e632ac/deploy
curl --fail --silent --show-error --location "$base/deploy-crds.yaml" --output "$work/crds.yaml"
curl --fail --silent --show-error --location "$base/deploy-operator.yaml" --output "$work/operator.yaml"
python3 - "$work" <<'PY'
import hashlib, pathlib, sys, yaml
root = pathlib.Path(sys.argv[1])
for name, expected in {
    'crds.yaml': 'c706530ddef7192fb7383f5445aa169475c6ae2e4961a8f130f890efd99d23ce',
    'operator.yaml': '115497b7e6b55e141b942b45f8dc1a4669fd5cb4381c06a873cfb454e3c5148d',
}.items():
    if hashlib.sha256((root/name).read_bytes()).hexdigest() != expected:
        raise SystemExit('MySQL controller manifest checksum did not match')
objects = list(yaml.safe_load_all((root/'operator.yaml').read_text()))
for obj in objects:
    if not obj or obj['kind'] != 'Deployment':
        continue
    pod = obj['spec']['template']['spec']
    pod['nodeSelector'] = {'kubernetes.io/arch': 'amd64'}
    container = pod['containers'][0]
    container['image'] += '@sha256:01ecaa57bf952850ff9ffd7caeb231a9c3335764fdb12488f132ea4605d8f364'
    container['resources'] = {'requests': {'cpu': '100m', 'memory': '256Mi'}, 'limits': {'cpu': '500m', 'memory': '512Mi'}}
    container['readinessProbe']['timeoutSeconds'] = 5
(root/'operator.yaml').write_text(yaml.safe_dump_all(objects))
PY
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --context "$context" apply --server-side -f "$work/crds.yaml"
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --context "$context" apply --server-side -f "$work/operator.yaml"
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --context "$context" rollout status deployment/mysql-operator -n mysql-operator --timeout=240s
