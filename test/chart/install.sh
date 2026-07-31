#!/usr/bin/env bash
# Installs the chart into a live cluster and asserts the operator it deploys
# publishes devices for a fleet declared in values.
#
# Separate from the Go e2e suite on purpose: that suite deploys the operator
# with `make deploy`, and a second operator installed by Helm would be a second
# reconciler for the same cluster-scoped GPUPools.
#
# The namespace is deliberately not ghostgpu-system. The kustomize overlay
# hardcodes that name, so installing there would pass even if the namespace
# substitution had silently failed.
set -euo pipefail

HELM="${HELM:-./bin/helm}"
NAMESPACE="${NAMESPACE:-ghostgpu-chart-test}"
RELEASE="${RELEASE:-ghostgpu}"
IMAGE="${IMAGE:-example.com/ghostgpu}"
TAG="${TAG:-v0.0.1}"
NODE="${NODE:-ghost-chart-0}"

cleanup() {
  "$HELM" uninstall "$RELEASE" -n "$NAMESPACE" 2>/dev/null || true
  kubectl delete node "$NODE" --ignore-not-found
  kubectl delete crd gpupools.ghostgpu.dev gpumodels.ghostgpu.dev --ignore-not-found
  kubectl delete ns "$NAMESPACE" --ignore-not-found
}
trap cleanup EXIT

# A kwok node for the pool to match. The name is unique across every suite and
# example in this repo: a collision means one suite's cleanup deletes another's
# node partway through its assertions.
kubectl apply -f - <<YAML
apiVersion: v1
kind: Node
metadata:
  name: ${NODE}
  annotations:
    kwok.x-k8s.io/node: fake
  labels:
    type: kwok
    chart-test: "true"
spec:
  taints:
    - key: kwok.x-k8s.io/node
      value: fake
      effect: NoSchedule
YAML

"$HELM" install "$RELEASE" charts/ghostgpu \
  --namespace "$NAMESPACE" --create-namespace \
  --set image.repository="$IMAGE" \
  --set image.tag="$TAG" \
  --set gpuModels[0].name=chart-h100 \
  --set gpuModels[0].spec.productName="NVIDIA-H100-80GB-HBM3" \
  --set gpuModels[0].spec.memory=80Gi \
  --set gpuModels[0].spec.computeCapability="9.0" \
  --set gpuPools[0].name=chart-pool \
  --set gpuPools[0].spec.modelRef=chart-h100 \
  --set gpuPools[0].spec.gpusPerNode=4 \
  --set-string gpuPools[0].spec.nodeSelector.chart-test="true" \
  --wait --timeout 3m

# The operator landed where Helm was told to put it, not where the kustomize
# overlay hardcodes. This is the assertion that catches a failed namespace
# substitution, and it is the reason the namespace above is not ghostgpu-system.
kubectl get deployment -n "$NAMESPACE" ghostgpu-controller-manager

# The claim under test: a fleet declared in values reaches the API server and
# the operator publishes devices for it.
for _ in $(seq 1 30); do
  published="$(kubectl get gpupool chart-pool -o jsonpath='{.status.devicesPublished}' 2>/dev/null || echo)"
  if [ "${published:-0}" -eq 4 ]; then
    echo "PASS: the pool published 4 devices"
    exit 0
  fi
  sleep 5
done

echo "FAIL: chart-pool never published 4 devices (last saw '${published:-none}')" >&2
kubectl get gpupool chart-pool -o yaml >&2
kubectl logs -n "$NAMESPACE" deployment/ghostgpu-controller-manager --tail=50 >&2
exit 1
