#!/usr/bin/env bash
# Generates the Helm chart from the same kustomize output that produces
# install.yaml, so the two cannot describe different operators.
#
# CRDs go to crds/ rather than templates/: Helm resolves REST mappings for the
# whole release before creating anything, so a GPUPool in templates/ beside its
# own CRD fails on first install. Verified in
# docs/superpowers/specs/2026-07-31-helm-crd-ordering-spike.md. The cost is that
# `helm upgrade` will not update CRDs, which is documented in the chart README.
set -euo pipefail

KUSTOMIZE="${KUSTOMIZE:-./bin/kustomize}"
CHART_DIR="${CHART_DIR:-charts/ghostgpu}"

NS_SENTINEL="ghostgpu-helm-release-namespace-sentinel"
IMG_SENTINEL="ghostgpu-helm-image-sentinel:sentinel"

# Only the generated subtrees are cleared. templates/fleet.yaml is hand-written
# and must survive regeneration.
rm -rf "${CHART_DIR}/crds"
rm -rf "${CHART_DIR}/templates/generated"
mkdir -p "${CHART_DIR}/crds" "${CHART_DIR}/templates/generated"

RENDERED="$(mktemp)"
trap 'rm -f "$RENDERED"' EXIT
"${KUSTOMIZE}" build config/helm > "${RENDERED}"

# Split by kind. CRDs are cluster-scoped and never carry the namespace, so they
# are written before any substitution touches them.
python3 - "$RENDERED" "$CHART_DIR" "$NS_SENTINEL" "$IMG_SENTINEL" <<'PY'
import sys, pathlib, yaml

rendered, chart_dir, ns_sentinel, img_sentinel = sys.argv[1:5]
chart = pathlib.Path(chart_dir)

docs = [d for d in yaml.safe_load_all(open(rendered)) if d]
if not docs:
    sys.exit("kustomize produced no documents")

crds, templates = [], []
for d in docs:
    (crds if d["kind"] == "CustomResourceDefinition" else templates).append(d)

if len(crds) != 2:
    sys.exit(f"expected the GPUModel and GPUPool CRDs, found {len(crds)}")

def dump(doc):
    return yaml.safe_dump(doc, default_flow_style=False, sort_keys=False)

for doc in crds:
    name = doc["metadata"]["name"]
    text = dump(doc)
    if ns_sentinel in text:
        sys.exit(f"CRD {name} carries a namespace; CRDs are cluster-scoped")
    (chart / "crds" / f"{name}.yaml").write_text(text)

# One file per kind+name keeps a diff readable and makes a missing object
# obvious in review, which a single concatenated manifest does not.
for doc in templates:
    # Helm owns namespace creation through --create-namespace. A chart that
    # ships its own Namespace object cannot be installed into an existing one.
    if doc["kind"] == "Namespace":
        continue

    text = dump(doc)
    text = text.replace(ns_sentinel, "{{ .Release.Namespace }}")
    text = text.replace(
        img_sentinel,
        '{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}',
    )
    kind = doc["kind"].lower()
    name = doc["metadata"]["name"]
    (chart / "templates" / "generated" / f"{kind}-{name}.yaml").write_text(text)

print(f"wrote {len(crds)} CRDs and {len(templates)} templates")
PY

# A sentinel surviving into the chart means a substitution was missed, which
# would install resources into a namespace named after the sentinel.
if grep -rq "$NS_SENTINEL\|ghostgpu-helm-image-sentinel" "${CHART_DIR}"; then
  echo "a sentinel survived generation:" >&2
  grep -rn "$NS_SENTINEL\|ghostgpu-helm-image-sentinel" "${CHART_DIR}" >&2
  exit 1
fi
