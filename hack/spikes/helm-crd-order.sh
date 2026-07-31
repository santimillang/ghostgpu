#!/usr/bin/env bash
# Does Helm install a CRD before a custom resource of that kind in the same
# release? The chart layout in the distribution spec assumes it does not.
#
# Aborts if the control (chart B, CRDs in crds/) fails, because a negative
# result from chart A means nothing if the mechanism is broken for both. A
# previous spike in this repo reported "eviction does not work" when the real
# cause was an invalid CEL field in the control path.
set -euo pipefail

WORK="$(mktemp -d)"
trap 'helm uninstall crdorder-a 2>/dev/null || true; helm uninstall crdorder-b 2>/dev/null || true; kubectl delete crd widgets.spike.ghostgpu.dev --ignore-not-found; rm -rf "$WORK"' EXIT

crd_yaml() {
  cat <<'YAML'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.spike.ghostgpu.dev
spec:
  group: spike.ghostgpu.dev
  names: {kind: Widget, listKind: WidgetList, plural: widgets, singular: widget}
  scope: Cluster
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                size: {type: integer}
YAML
}

cr_yaml() {
  cat <<'YAML'
apiVersion: spike.ghostgpu.dev/v1
kind: Widget
metadata:
  name: spike-widget
spec:
  size: 1
YAML
}

chart_scaffold() {
  local dir="$1" name="$2"
  mkdir -p "$dir/templates" "$dir/crds"
  cat > "$dir/Chart.yaml" <<YAML
apiVersion: v2
name: $name
version: 0.0.1
YAML
}

# Chart B first: it is the control. If the mechanism the spec relies on does
# not work, chart A's result is uninterpretable.
chart_scaffold "$WORK/b" crdorder-b
crd_yaml > "$WORK/b/crds/widget-crd.yaml"
cr_yaml  > "$WORK/b/templates/widget.yaml"

echo "== control: CRD in crds/, CR in templates/ =="
if ! helm install crdorder-b "$WORK/b" --wait --timeout 2m; then
  echo "CONTROL FAILED: the crds/ layout does not install. Nothing below is interpretable." >&2
  exit 1
fi
kubectl get widget spike-widget -o name
helm uninstall crdorder-b
kubectl delete crd widgets.spike.ghostgpu.dev --ignore-not-found
kubectl wait --for=delete crd/widgets.spike.ghostgpu.dev --timeout=60s 2>/dev/null || true

echo
echo "== subject: CRD and CR both in templates/ =="
chart_scaffold "$WORK/a" crdorder-a
crd_yaml > "$WORK/a/templates/widget-crd.yaml"
cr_yaml  > "$WORK/a/templates/widget.yaml"

if helm install crdorder-a "$WORK/a" --wait --timeout 2m; then
  echo "RESULT: templated CRDs DO work alongside their CRs. The spec's premise is wrong."
  kubectl get widget spike-widget -o name
else
  echo "RESULT: templated CRDs fail alongside their CRs, as the spec assumes."
fi
