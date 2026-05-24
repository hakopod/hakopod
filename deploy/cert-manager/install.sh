#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then
  echo 'Usage: bash deploy/cert-manager/install.sh KUBECONFIG CONTEXT' >&2
  exit 2
fi
module_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
task_kubeconfig=$1
task_context=$2
kubectl --kubeconfig "$task_kubeconfig" --context "$task_context" get --raw /readyz >/dev/null
owner=$(kubectl --kubeconfig "$task_kubeconfig" --context "$task_context" get crd certificates.cert-manager.io --ignore-not-found --output 'jsonpath={.metadata.annotations.meta\.helm\.sh/release-name}')
exists=$(kubectl --kubeconfig "$task_kubeconfig" --context "$task_context" get crd certificates.cert-manager.io --ignore-not-found --output name)
if [ -n "$exists" ] && [ "$owner" != 'hakopod-cert-manager' ]; then
  echo 'Existing cert-manager CRDs belong to another installation. Manage that installation explicitly.' >&2
  exit 1
fi
(
  cd "$module_dir"
  shasum -a 256 -c checksums.sha256
)
helm upgrade --install hakopod-cert-manager "$module_dir/cert-manager-v1.21.2.tgz" \
  --kubeconfig "$task_kubeconfig" --kube-context "$task_context" \
  --namespace cert-manager --create-namespace --values "$module_dir/values.yaml" \
  --wait --timeout 3m
kubectl --kubeconfig "$task_kubeconfig" --context "$task_context" \
  wait --for=condition=Established crd/clusterissuers.cert-manager.io --timeout=30s
echo 'cert-manager is installed. Create an issuer and inspect its actual Ready condition before requesting certificates.'
