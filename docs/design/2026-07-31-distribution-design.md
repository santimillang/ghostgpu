# Distribution: CLI binaries, a Helm chart, and a signed release

Date: 2026-07-31
Status: approved, not implemented

## Problem

ghostgpu has an operator image and a single-file installer, and nothing else. Two
consequences, both of which stop someone evaluating it:

- **The CLI is source-only.** The README's own quickstart installs the operator
  from a release URL and then says `make build-cli`, which requires a clone.
  Nobody can follow it end to end. `ghostgpu up` is how a fleet gets declared, so
  the CLI is not optional tooling — it is the second step of the tutorial.
- **There is no chart.** A platform engineer building a test cluster composes it
  out of charts already: Kueue, kube-prometheus-stack, their own operator. A
  project installed by a raw URL is the odd one out in that script.

Neither is a capability gap. Both are why nobody can use what already works.

## Scope

In: GoReleaser for CLI binaries, a Helm chart, supply-chain attestation, a
`ghostgpu version` command, and the CI that keeps all of it honest.

Out, deliberately: Homebrew tap and krew (permanent maintenance for zero current
users — they go in when someone asks); the docs site; the README rewrite and
demo, which are their own piece of work; the release *decision*, which is Santi's.

## Decisions taken before design

**The API group stays `ghostgpu.dev` although the domain is unregistered.** The
Kubernetes convention is a group under a domain you control, and we are knowingly
departing from it. The API is `v1alpha1` with no compatibility guarantee, the
probability of someone else registering the domain is low, and the cost of being
wrong is a group rename while the user count is small. Recorded here so it reads
as a decision rather than an oversight.

## Architecture

One tag, `v*`, publishes four artifacts:

| Artifact | Where | Status |
|---|---|---|
| Manager image (multi-arch) | `ghcr.io/santimillang/ghostgpu:<version>` | exists |
| `install.yaml` | Release asset | exists |
| CLI binaries + checksums | Release assets, via GoReleaser | new |
| Helm chart | `oci://ghcr.io/santimillang/charts/ghostgpu` + `gh-pages` index | new |

`install.yaml` is unchanged. It is the zero-dependency path, it works, and the
chart does not replace it.

### The chart is generated, not written

The chart's operator manifests and `install.yaml` come from different generators,
so they can diverge silently. Rather than police that, the chart is **generated
from the same kustomize output**: `make helm` runs `kustomize build config/helm`,
splits the documents by kind into `chart/crds/` and `chart/templates/`, and
substitutes two sentinels.

Sentinels, not pattern-matching. `config/helm` sets the namespace to a literal
that appears nowhere else in the repo, and the substitution replaces exactly that
string with `{{ .Release.Namespace }}`. Scripted literal replacement across
generated files has produced four self-inflicted bugs in this repo by also
rewriting the declaration it was meant to preserve; a unique sentinel is the
defense.

Two substitutions:

- namespace sentinel → `{{ .Release.Namespace }}` (the `config/default` overlay
  hardcodes `ghostgpu-system`; a chart that ignores `helm install -n` is broken)
- image sentinel → `{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}`

`namePrefix: ghostgpu-` is kept as-is. Chart convention would template it from
the release name, but the names appear in `install.yaml`, in the e2e suite, and
in every troubleshooting instruction we have written. One project, one set of
object names, is worth more than the convention.

### CRDs live in `crds/`, not `templates/`

Helm resolves REST mappings for every rendered manifest before it creates
anything. A `GPUPool` in `templates/` beside its own CRD in `templates/` is
therefore expected to fail on first install with *no matches for kind* — the CRD
does not exist when the mapping is resolved. `crds/` avoids this: Helm installs
that directory first and refreshes discovery.

The cost is Helm's known wart — `helm upgrade` does not update CRDs, so an API
change requires `kubectl apply -f` of the CRDs by hand. That trade is acceptable
here specifically because ghostgpu runs on ephemeral kwok clusters, where users
reinstall far more often than they upgrade, and because a first-install failure is
the first thing a new user would experience. cert-manager makes the same trade.

**This ordering behaviour is an expectation, not a verified fact.** Task 1 of the
implementation plan is a spike that puts a CRD and a CR of that kind in one
chart's `templates/` and installs it against a live cluster. If it succeeds, the
layout is revisited before anything is built on it.

## The values contract

```yaml
image:
  repository: ghcr.io/santimillang/ghostgpu
  tag: ""                 # defaults to .Chart.AppVersion
  pullPolicy: IfNotPresent

# Rendered verbatim into GPUModel/GPUPool objects. productName becomes a
# Kubernetes node label value (internal/gpu/nodelabels.go), so it must be a
# valid label value: no spaces.
gpuModels: []
# - name: h100
#   spec:
#     productName: NVIDIA-H100-80GB-HBM3
#     memory: 80Gi
#     computeCapability: "9.0"

gpuPools: []
# - name: h100-pool
#   spec:
#     modelRef: h100
#     gpusPerNode: 8
#     occupancy:
#       - busyPerNode: 2
```

Each entry is `{name, spec}`: `name` becomes `metadata.name`, `spec` is emitted
with `toYaml` and otherwise untouched. Both CRs are cluster-scoped, so no
namespace is templated onto them.

**The chart validates nothing about `spec`.** The CRD's OpenAPI schema is the
validator. A `values.schema.json` describing the same fields would be a second
copy of the API that drifts from the first, which is precisely what passthrough
exists to avoid. This means a typo in `spec` is caught by the API server at
install time rather than by Helm at render time — a worse error message, accepted
knowingly in exchange for having one schema.

Deliberately absent: replicas, resource limits, nodeSelector, tolerations,
affinity, PodDisruptionBudget. Standard chart boilerplate nobody tunes on a
simulator, and each is a template branch to maintain forever. They go in when
someone asks.

## GoReleaser

`.goreleaser.yaml` builds `./cmd/ghostgpu` for `linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64`, `windows/amd64`.

Windows is included and the operator is not: `up`, `status`, and `capture` are
plain API-server clients with no Linux dependency, and the person driving a
simulator from a Windows laptop is a real user. The manager stays Linux-only.

`ghostgpu version` is added — currently the CLI cannot report its own version,
which is the first thing anyone does with a downloaded binary and the first thing
any bug report needs. Version, commit, and build date are injected by ldflags,
defaulting to `dev` for `make build-cli`.

## Supply chain

The set that stops a security-conscious platform team from asking:

- **cosign keyless signing** of the manager image (OIDC, no key to manage).
- **Build provenance attestation** for the image and the release archives, via
  `actions/attest-build-provenance`.
- **SBOM**: syft for the binaries through GoReleaser, and buildx SBOM for the
  image.

The release notes carry the exact `cosign verify` invocation, and CI runs that
verification once against the dry-run build. A signing story nobody has ever
verified is worse than no signing story, because it is a claim rather than a
control.

## Failure modes

**The release workflow has never run — not even `workflow_dispatch`.** The first
tag would be its first execution. A `workflow_dispatch` dry run is a mandatory
gate before any tag is pushed.

**ghcr package visibility is separate from repository visibility.** Both the
image and the chart packages default to inheriting private, and an anonymous
`docker pull` or `helm pull` against them fails with a bare `denied`. Both must be
set public explicitly. This is the most likely first-user complaint and it is
invisible from inside the repo.

**Partial publication.** The workflow pushes an image, then binaries, then the
chart. A failure after the image is pushed leaves an immutable tag that a re-run
cannot replace. Ordering is therefore cheapest-to-redo last, and re-tagging
requires bumping the patch version rather than moving a tag — documented in
CONTRIBUTING.

**Chart rot.** Without an install path exercised by CI, the chart is stale within
two PRs.

## Testing

| Layer | Assertion |
|---|---|
| CI | Chart generation is deterministic; `make helm` produces no diff against the committed chart. |
| CI | `helm template` and `install.yaml` agree on kinds, RBAC rules, and image reference. |
| CI | `helm lint` passes; the chart renders with empty values and with a fleet. |
| e2e | `helm install` into a non-default namespace with `gpuPools` set publishes devices against a live cluster. |
| Manual gate | `workflow_dispatch` dry run succeeds before the first tag. |

The e2e Helm install is not optional. Every other check is about the chart's
text; only this one is about whether it works.

The e2e suite builds the manager image and loads it into kind, so the chart
install there must set `image.repository` and `image.tag` to that locally loaded
reference. Otherwise the release pulls from ghcr and the test silently exercises
a published version rather than the code under test.

## What this does not fix

The README still tells people to `make build-cli`. Fixing it is the next piece of
work, along with the demo — deliberately after this one, so the instructions can
point at artifacts that exist.
