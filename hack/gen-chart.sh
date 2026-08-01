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

# Both sentinels must be present in what kustomize just rendered, BEFORE any
# substitution runs. config/helm's own images transformer only matches an
# image literally named "controller" (see config/helm/kustomization.yaml). If
# config/manager/kustomization.yaml already carries an `images:` override —
# which `make build-installer` and `make test-e2e` both write via
# `kustomize edit set image` — the image in config/manager is no longer named
# "controller" by the time config/helm's overlay runs, so config/helm's
# transformer silently matches nothing and the sentinel is never introduced.
# The chart then hardcodes whatever image kustomization.yaml last set, and
# `image.repository`/`image.tag` become inert. Checking only whether a
# sentinel SURVIVED (below) cannot catch this, because it never arrived.
if ! grep -q "$NS_SENTINEL" "$RENDERED" || ! grep -q "$IMG_SENTINEL" "$RENDERED"; then
  {
    echo "gen-chart: kustomize did not emit one or both sentinels."
    grep -q "$NS_SENTINEL" "$RENDERED" || echo "  missing namespace sentinel ($NS_SENTINEL)"
    grep -q "$IMG_SENTINEL" "$RENDERED" || echo "  missing image sentinel ($IMG_SENTINEL)"
    echo
    echo "Likely cause: config/manager/kustomization.yaml has a leftover"
    echo "'images:' override (from 'kustomize edit set image', e.g. via"
    echo "'make build-installer' or 'make test-e2e'). That renames the"
    echo "controller image before config/helm's own image transformer runs,"
    echo "so config/helm's matcher (name: controller) finds nothing and the"
    echo "chart would hardcode a fixed image instead of a Helm value."
    echo "Fix: 'git checkout -- config/manager/kustomization.yaml' (or"
    echo "otherwise remove the images: override) and re-run 'make helm'."
  } >&2
  exit 1
fi

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
