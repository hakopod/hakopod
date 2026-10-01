#!/usr/bin/env bash
# Install the tested database controllers only into the named development cluster.
set -euo pipefail
: "${HAKOPOD_TEST_KUBECONFIG:?Set HAKOPOD_TEST_KUBECONFIG to the development kubeconfig}"
: "${HAKOPOD_REDIS_CONTROLLER_IMAGE:?Build Dockerfile.redis-controller and supply its tag@sha256 image reference}"
[[ "$HAKOPOD_REDIS_CONTROLLER_IMAGE" =~ ^[^[:space:]]+:[^/@:]+@sha256:[a-f0-9]{64}$ ]] || { echo 'Redis controller image must include a tag and SHA-256 digest' >&2; exit 1; }
redis_tagged="${HAKOPOD_REDIS_CONTROLLER_IMAGE%@*}"
redis_repository="${redis_tagged%:*}"
redis_tag="${redis_tagged##*:}@${HAKOPOD_REDIS_CONTROLLER_IMAGE#*@}"
context=k3d-hakopod-dev
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" config get-contexts -o name | grep -qx "$context"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl --fail --silent --show-error --location https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v1.30.1/cnpg-1.30.1.yaml --output "$work/cnpg.yaml"
python3 - "$work/cnpg.yaml" <<'PY'
import hashlib,pathlib,sys
path=pathlib.Path(sys.argv[1])
if hashlib.sha256(path.read_bytes()).hexdigest() != '37237f145d8138256ea25ae830f87759255665ff08f8d552fdd8224a5ec032fb':
    raise SystemExit('CloudNativePG manifest checksum did not match')
image='ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1'
path.write_text(path.read_text().replace(image,image+'@sha256:923c267ec29636db3bee20f993d0ec4973fa22998e1adad37da79e4d32b5bc07'))
PY
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --context "$context" apply --server-side -f "$work/cnpg.yaml"
helm pull redis-operator --repo https://ot-container-kit.github.io/helm-charts/ --version 0.26.0 --destination "$work"
python3 - "$work/redis-operator-0.26.0.tgz" <<'PY'
import hashlib,pathlib,sys
if hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest() != '75c22256179e2d2e58bb9df23ffff3b69a90db0d7fe1384099bcf220a70c090b':
    raise SystemExit('Redis operator chart checksum did not match')
PY
# The 0.26 chart still emits AvoidCommandLinePassword although the operator has
# removed that feature gate. Omitting it is required for the operator to start.
helm upgrade --install redis-operator "$work/redis-operator-0.26.0.tgz" \
 --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --kube-context "$context" \
 --namespace redis-operator --create-namespace --reset-values \
 --set-string redisOperator.imageName="$redis_repository" --set-string redisOperator.imageTag="$redis_tag" \
 --set-string redisOperator.initContainerImageTag="$redis_tag" --set redisOperator.imagePullPolicy=IfNotPresent \
 --set-string 'redisOperator.podAnnotations.hakopod\.io/redis-controller-source=c5017206e75f7743d79e82db47ec8c39d7410816' \
 --set-string 'redisOperator.podAnnotations.hakopod\.io/redis-tls-policy=ca-verified-v1' \
 --set featureGates.AvoidCommandLinePassword=null \
 --set featureGates.GenerateConfigInInitContainer=true \
 --set manager.config.maxConcurrentReconciles=1 \
 --set manager.config.execCommandTimeout=20m \
 --set resources.requests.cpu=100m --set resources.requests.memory=128Mi \
 --set resources.limits.cpu=500m --set resources.limits.memory=384Mi \
 --wait --timeout 180s
kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --context "$context" rollout status deployment/cnpg-controller-manager -n cnpg-system --timeout=180s
