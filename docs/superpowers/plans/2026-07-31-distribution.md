# Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship ghostgpu's CLI as prebuilt binaries and the operator as a Helm chart, signed and attested, so nobody has to clone the repo to use it.

**Architecture:** GoReleaser builds the CLI for five platforms on tag. The Helm chart is *generated* from the existing kustomize output rather than hand-written, with two unique sentinels substituted for Helm templating, so it cannot drift from `install.yaml`. CRDs ship in the chart's `crds/` directory; user-declared fleets pass through `values.yaml` verbatim into `GPUModel`/`GPUPool` objects.

**Tech Stack:** Go 1.26, GoReleaser, Helm 3, kustomize v5.8.1, cosign, `actions/attest-build-provenance`, kwok + kind for live verification.

**Spec:** `docs/superpowers/specs/2026-07-31-distribution-design.md`

## Global Constraints

- **Never commit to `main`.** One branch per task, PR with a substantive description, CI green, rebase-merge. Santi gates every commit and push — ask before committing.
- **Conventional Commits, DCO sign-off (`git commit -s`)**, body explaining *why*, `Co-Authored-By` trailer.
- **TDD:** write the failing test, confirm red, minimal implementation, confirm green.
- **Fix lint properly, never suppress it.** The repo runs a custom golangci-lint build (`.custom-gcl.yml`).
- **Go is not on the default WSL login PATH.** Use the `~/gg.sh` wrapper to run `go` and `make`.
- **`make build-cli` does not rebuild `bin/manager`,** and `go build ./...` writes nothing to `bin/`. A stale operator binary has cost a debugging cycle before.
- **`make test-e2e` leaves `config/manager/kustomization.yaml` modified** (`kustomize edit set image`). Discard it before committing.
- **`make test-e2e` only tears the cluster down on success.** Anything created with a bare `kubectl create` poisons every later run.
- **Docker Desktop rewrites `~/.docker/config.json` with `credsStore: desktop.exe`,** which WSL cannot exec; kind then silently *builds* the node image for 10+ minutes. Fix with `printf '{}' > ~/.docker/config.json`.
- **Never script a literal→constant replacement across a Go file.** It rewrites the `const` declaration into `x = x`, an initialization cycle. Four occurrences so far.
- **API group stays `ghostgpu.dev`.** Decided; do not change it in this work.
- **Node names must be unique across all e2e suites and examples.** A collision makes either suite's cleanup delete the other's node.

---

### Task 1: Spike — does Helm install a CRD before a CR of that kind?

The whole chart layout rests on an unverified belief: that Helm resolves REST mappings for every rendered manifest before creating anything, so a CRD and its CR cannot both live in `templates/`. If that is wrong, CRDs belong in `templates/` (where `helm upgrade` would update them) and the design improves. Settle it before building on it.

**Files:**
- Create: `hack/spikes/helm-crd-order.sh`
- Create: `docs/superpowers/specs/2026-07-31-helm-crd-ordering-spike.md`

**Interfaces:**
- Consumes: nothing.
- Produces: a verdict that Task 4 and Task 5 depend on. If templated CRDs work, stop and revise the spec before continuing.

- [ ] **Step 1: Write the spike script**

Two throwaway charts against a live cluster. Chart A puts the CRD in `templates/` beside a CR of that kind — the layout under suspicion. Chart B puts the CRD in `crds/` — the layout the spec assumes. The CRD is a trivial one of our own, not ghostgpu's, so the spike cannot disturb a real install.

```bash
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
```

- [ ] **Step 2: Make it executable and run it against a live cluster**

```bash
chmod +x hack/spikes/helm-crd-order.sh
make setup-test-e2e        # a kwok+kind cluster; any real cluster works
./hack/spikes/helm-crd-order.sh
```

Expected: the control installs and prints `widget.spike.ghostgpu.dev/spike-widget`, then the subject fails with a message containing `no matches for kind "Widget"`.

- [ ] **Step 3: Record the findings**

Write `docs/superpowers/specs/2026-07-31-helm-crd-ordering-spike.md` with: the Helm version tested (`helm version --short`), the Kubernetes version, the verbatim error or success output from both charts, and the verdict.

**If the subject chart succeeded, stop here.** Report to Santi that the spec's premise is wrong and that CRDs should move to `templates/` (making `helm upgrade` work), and get the spec amended before starting Task 4.

- [ ] **Step 4: Tear down and commit**

```bash
make cleanup-test-e2e
git add hack/spikes/helm-crd-order.sh docs/superpowers/specs/2026-07-31-helm-crd-ordering-spike.md
git commit -s -m "docs: verify Helm's CRD and CR ordering before designing the chart"
```

---

### Task 2: `ghostgpu version`

Independent of everything else, and the reason to do it now is that GoReleaser injects the values it reports. A binary that cannot say what it is makes every bug report start with a question.

**Files:**
- Create: `internal/cli/version.go`
- Create: `internal/cli/version_test.go`
- Modify: `cmd/ghostgpu/main.go` (the `usage` const, `run`'s switch, and new package-level build vars)
- Modify: `Makefile` (`build-cli` ldflags)

**Interfaces:**
- Produces:
  - `cli.BuildInfo` — struct with string fields `Version`, `Commit`, `Date`.
  - `cli.VersionLine(b BuildInfo) string` — the single line `ghostgpu version` prints.
  - `main.version`, `main.commit`, `main.date` — package-level `string` vars in `cmd/ghostgpu/main.go`. **These exact names matter:** GoReleaser's default ldflags target `main.version`, `main.commit`, and `main.date`, so Task 3 needs no ldflags configuration at all.

- [ ] **Step 1: Write the failing test**

`internal/cli/version_test.go`:

```go
package cli_test

import (
	"testing"

	"github.com/santimillang/ghostgpu/internal/cli"
)

// A released binary reports exactly what it was built from, because that is the
// first line of any bug report.
func TestVersionLineReportsInjectedBuildInfo(t *testing.T) {
	got := cli.VersionLine(cli.BuildInfo{
		Version: "v0.1.0",
		Commit:  "0b289ac",
		Date:    "2026-07-31T12:00:00Z",
	})

	want := "ghostgpu v0.1.0 (0b289ac, built 2026-07-31T12:00:00Z)"
	if got != want {
		t.Errorf("VersionLine() = %q, want %q", got, want)
	}
}

// A binary built with `make build-cli` or `go build` has nothing injected. It
// must say so rather than print an empty version that reads like a released
// one, because "ghostgpu  ()" in an issue is worse than no version at all.
func TestVersionLineSaysDevWhenNothingWasInjected(t *testing.T) {
	got := cli.VersionLine(cli.BuildInfo{})

	want := "ghostgpu dev (unknown commit, built at an unknown time)"
	if got != want {
		t.Errorf("VersionLine() = %q, want %q", got, want)
	}
}

// Partial injection is the realistic failure: a build system that sets the
// version but not the date should still produce a readable line.
func TestVersionLineFillsOnlyTheMissingParts(t *testing.T) {
	got := cli.VersionLine(cli.BuildInfo{Version: "v0.2.0"})

	want := "ghostgpu v0.2.0 (unknown commit, built at an unknown time)"
	if got != want {
		t.Errorf("VersionLine() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
~/gg.sh go test ./internal/cli/ -run TestVersionLine -v
```

Expected: FAIL — `undefined: cli.VersionLine` and `undefined: cli.BuildInfo`.

- [ ] **Step 3: Write the minimal implementation**

`internal/cli/version.go`:

```go
package cli

import "fmt"

// BuildInfo describes the binary the user is actually running.
//
// The fields are populated by ldflags at release time and left empty by a plain
// `go build`, so every field has to render sensibly when unset.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// VersionLine renders BuildInfo as the one line `ghostgpu version` prints.
//
// Unset fields are named rather than blank. An empty version reads like a
// released binary that forgot to say which release, which is the one thing this
// command exists to prevent.
func VersionLine(b BuildInfo) string {
	version := b.Version
	if version == "" {
		version = "dev"
	}

	commit := "unknown commit"
	if b.Commit != "" {
		commit = b.Commit
	}

	built := "built at an unknown time"
	if b.Date != "" {
		built = "built " + b.Date
	}

	return fmt.Sprintf("ghostgpu %s (%s, %s)", version, commit, built)
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
~/gg.sh go test ./internal/cli/ -run TestVersionLine -v
```

Expected: PASS, all three.

- [ ] **Step 5: Lint**

```bash
~/gg.sh make lint
```

Expected: no findings. The repo runs a custom golangci-lint build; fix anything it reports rather than suppressing it.

- [ ] **Step 6: Wire the subcommand into the CLI**

In `cmd/ghostgpu/main.go`, add the build vars below the imports:

```go
// Populated by ldflags at release time. GoReleaser writes these exact symbols
// by default, so the release configuration needs no ldflags of its own.
var (
	version = "dev"
	commit  = ""
	date    = ""
)
```

Add the line to the `usage` const, after the `capture` line:

```
  ghostgpu version          print the version of this binary
```

Add to `run`'s switch, before `default`:

```go
	case "version", "--version":
		say(stdout, "%s\n", cli.VersionLine(cli.BuildInfo{
			Version: version,
			Commit:  commit,
			Date:    date,
		}))
		return nil
```

`--version` is accepted alongside the subcommand because half of users will type it that way and an error there is a pointless first impression. `-v` is deliberately not accepted: it reads as verbose.

- [ ] **Step 7: Add ldflags to the local build**

In the `Makefile`, replace the `build-cli` recipe body:

```make
.PHONY: build-cli
build-cli: manifests generate fmt vet ## Build the ghostgpu CLI.
	go build -ldflags "-X main.version=$(CLI_VERSION) -X main.commit=$(CLI_COMMIT) -X main.date=$(CLI_DATE)" -o bin/ghostgpu ./cmd/ghostgpu
```

And near `IMG ?=` at the top of the file:

```make
# Injected into the CLI so a locally built binary still reports where it came
# from. A release overrides these through GoReleaser's defaults.
CLI_VERSION ?= dev
CLI_COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
CLI_DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
```

- [ ] **Step 8: Verify the wiring by hand**

```bash
~/gg.sh make build-cli
./bin/ghostgpu version
./bin/ghostgpu --version
```

Expected: both print `ghostgpu dev (<short sha>, built <timestamp>)`. Confirm the sha matches `git rev-parse --short HEAD`.

- [ ] **Step 9: Run the full unit suite and lint**

```bash
~/gg.sh make test
~/gg.sh make lint
```

Expected: PASS and no lint findings.

- [ ] **Step 10: Commit**

```bash
git add internal/cli/version.go internal/cli/version_test.go cmd/ghostgpu/main.go Makefile
git commit -s -m "feat: report the CLI's own version

A downloaded binary could not say what it was built from, which is the
first thing any bug report needs and the first thing anyone runs after
unpacking an archive. Unset fields are named rather than blank: an empty
version reads like a release that forgot to identify itself."
```

---

### Task 3: GoReleaser builds the CLI

**Files:**
- Create: `.goreleaser.yaml`
- Modify: `.github/workflows/release.yml`
- Modify: `.gitignore` (GoReleaser's `dist/` is already ignored — confirm, do not duplicate)

**Interfaces:**
- Consumes: `main.version`, `main.commit`, `main.date` from Task 2.
- Produces: release assets `ghostgpu_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows) and `checksums.txt`. Task 9 attests these; Task 10 documents them.

- [ ] **Step 1: Write the GoReleaser configuration**

`.goreleaser.yaml`:

```yaml
# The CLI is half the quickstart: `ghostgpu up` is how a fleet gets declared, so
# shipping only the operator image left the tutorial requiring a clone.
version: 2

project_name: ghostgpu

before:
  hooks:
    - go mod tidy

builds:
  - id: ghostgpu
    main: ./cmd/ghostgpu
    binary: ghostgpu
    env:
      - CGO_ENABLED=0
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ignore:
      # No Windows on ARM: nobody is running a Kubernetes test harness there,
      # and an untested binary is worse than an absent one.
      - goos: windows
        goarch: arm64
    # ldflags are GoReleaser's defaults, which write main.version, main.commit
    # and main.date. Task 2 named those symbols to match.

archives:
  - id: ghostgpu
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files:
      - LICENSE
      - README.md

checksum:
  name_template: checksums.txt

# The GitHub Release is created by the workflow's existing publish step, which
# also attaches install.yaml. GoReleaser must not create or replace it.
release:
  disable: false
  mode: append

changelog:
  disable: true
```

The Windows build is included and the operator's is not: `up`, `status`, and `capture` are plain API-server clients, and driving a simulator from a Windows laptop is a real workflow. The manager stays Linux-only.

- [ ] **Step 2: Verify the config parses and builds locally, without releasing**

```bash
~/gg.sh go run github.com/goreleaser/goreleaser/v2@latest check
~/gg.sh go run github.com/goreleaser/goreleaser/v2@latest build --snapshot --clean --single-target
```

Expected: `check` reports the config is valid; `build` produces `dist/ghostgpu_linux_amd64_v1/ghostgpu`.

- [ ] **Step 3: Confirm the snapshot binary reports a version**

```bash
./dist/ghostgpu_linux_amd64_v1/ghostgpu version
```

Expected: a line containing a version, a commit, and a build date — **not** `ghostgpu dev (unknown commit, built at an unknown time)`. If it prints the all-unknown line, the ldflags are not reaching `main`, and the symbol names in Task 2 Step 6 are wrong. Fix that before continuing; this is the whole point of the step.

- [ ] **Step 4: Add the GoReleaser step to the release workflow**

In `.github/workflows/release.yml`, after the "Generate the installer" step and before "Check the installer parses and is complete", add:

```yaml
      # Ordered before the release publish step so a GoReleaser failure aborts
      # while nothing user-visible has been created. mode: append means it adds
      # assets to the release the publish step creates rather than owning it.
      - name: Build the CLI archives
        uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean ${{ github.event_name == 'workflow_dispatch' && '--snapshot' || '' }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

`--snapshot` on `workflow_dispatch` builds without publishing, which is what makes the dry run a real rehearsal instead of a partial release.

- [ ] **Step 5: Attach the archives to the dry-run artifact upload**

In the existing "Upload the installer for a dry run" step, extend the paths so a dry run produces something inspectable:

```yaml
        with:
          name: release-artifacts
          path: |
            dist/install.yaml
            dist/*.tar.gz
            dist/*.zip
            dist/checksums.txt
```

Note the `name` changes from `install.yaml` to `release-artifacts`; it now holds more than the installer.

- [ ] **Step 6: Confirm GoReleaser's output directory does not collide with the installer's**

`make build-installer` writes `dist/install.yaml`; GoReleaser runs `--clean`, which **empties `dist/`**. The workflow generates the installer *before* GoReleaser runs, so `--clean` would delete it.

Fix by reordering: move the "Generate the installer" and "Check the installer parses" steps to run *after* the GoReleaser step. Verify by reading the final workflow top to bottom and confirming `make build-installer` appears below `goreleaser`.

- [ ] **Step 7: Run the dry build on GitHub and inspect the artifacts**

```bash
gh.exe workflow run release.yml --repo santimillang/ghostgpu -f version=v0.0.0-dryrun
gh.exe run watch --repo santimillang/ghostgpu
```

Expected: green. Download the artifact and confirm it contains `install.yaml`, five archives, and `checksums.txt`.

**This is the first time the release workflow will ever have run.** Treat a failure here as expected rather than surprising.

- [ ] **Step 8: Commit**

```bash
git add .goreleaser.yaml .github/workflows/release.yml
git commit -s -m "feat: publish prebuilt CLI binaries

The quickstart told people to install the operator from a release URL and
then build the CLI from source, which cannot be followed without a clone.
Five platforms including windows/amd64: up, status and capture are plain
API-server clients, so the CLI has no Linux dependency the operator does."
```

---

### Task 4: Generate the chart from kustomize

**Files:**
- Create: `config/helm/kustomization.yaml`
- Create: `hack/gen-chart.sh`
- Create: `charts/ghostgpu/Chart.yaml`
- Create: `charts/ghostgpu/values.yaml`
- Create: `charts/ghostgpu/.helmignore`
- Modify: `Makefile` (a `helm` target, `HELM` tool binary and version)

**Interfaces:**
- Consumes: the Task 1 verdict (CRDs in `crds/`).
- Produces: `make helm`, which regenerates `charts/ghostgpu/crds/` and `charts/ghostgpu/templates/` from `config/helm`. Task 5 adds CR templates that the script must not delete; Task 6 asserts regeneration is a no-op.

- [ ] **Step 1: Create the kustomize overlay with sentinels**

`config/helm/kustomization.yaml`:

```yaml
# The Helm chart is generated from this overlay rather than hand-written, so it
# cannot drift from install.yaml. The two sentinels below are replaced with Helm
# template expressions by hack/gen-chart.sh.
#
# They are deliberately long and unique. This repo has produced four bugs from
# scripted literal replacement that also rewrote a declaration it was meant to
# leave alone; a string that appears nowhere else is the defense.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ../default

# Must stay a valid RFC 1123 label, because kustomize validates it.
namespace: ghostgpu-helm-release-namespace-sentinel

images:
  - name: controller
    newName: ghostgpu-helm-image-sentinel
    newTag: sentinel
```

- [ ] **Step 2: Verify the overlay builds and the sentinels appear**

```bash
~/gg.sh make kustomize
./bin/kustomize build config/helm | grep -c ghostgpu-helm-release-namespace-sentinel
./bin/kustomize build config/helm | grep -c ghostgpu-helm-image-sentinel
```

Expected: both counts are greater than zero. If the namespace sentinel count is zero, the overlay is not overriding `config/default`'s `ghostgpu-system`, and everything downstream is broken.

- [ ] **Step 3: Write the generation script**

`hack/gen-chart.sh`:

```bash
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
```

The final `grep` is the load-bearing check: a missed substitution would otherwise ship a chart that installs into a namespace literally named `ghostgpu-helm-release-namespace-sentinel`.

- [ ] **Step 4: Write the chart metadata and values**

`charts/ghostgpu/Chart.yaml`:

```yaml
apiVersion: v2
name: ghostgpu
description: Simulate GPU clusters on Kubernetes — test GPU-aware schedulers with zero GPU hardware
type: application
# version is the chart's own version; appVersion is the ghostgpu release it
# deploys. Both are rewritten by the release workflow from the tag.
version: 0.0.0-dev
appVersion: 0.0.0-dev
home: https://github.com/santimillang/ghostgpu
sources:
  - https://github.com/santimillang/ghostgpu
keywords: [kubernetes, gpu, dra, kwok, scheduler, simulation, testing]
maintainers:
  - name: santimillang
    url: https://github.com/santimillang
```

`charts/ghostgpu/values.yaml`:

```yaml
image:
  repository: ghcr.io/santimillang/ghostgpu
  # Defaults to the chart's appVersion, so the chart and the operator it
  # deploys stay in step unless deliberately overridden.
  tag: ""
  pullPolicy: IfNotPresent

# Fleets declared here are rendered verbatim into GPUModel and GPUPool objects.
# `spec` is passed through untouched: the CRD's OpenAPI schema is the validator,
# and a copy of it here would be a second API to keep in sync with the first.
#
# gpuModels:
#   - name: h100
#     spec:
#       productName: NVIDIA H100 80GB HBM3
#       memory: 80Gi
#       computeCapability: "9.0"
#
# gpuPools:
#   - name: h100-pool
#     spec:
#       modelRef: h100
#       gpusPerNode: 8
#       occupancy:
#         - busyPerNode: 2
gpuModels: []
gpuPools: []
```

`charts/ghostgpu/.helmignore`:

```
.DS_Store
*.tgz
```

- [ ] **Step 5: Add the Makefile target and the helm tool**

Next to the other tool binaries (`KUSTOMIZE ?=` etc.):

```make
HELM ?= $(LOCALBIN)/helm
```

Next to the other tool versions:

```make
# Pin to the newest Helm 3 release. Resolve it with:
#   curl -s https://api.github.com/repos/helm/helm/releases/latest | grep tag_name
# and record the exact tag here, because a floating version makes chart
# generation non-reproducible between a developer's machine and CI.
HELM_VERSION ?= v3.19.0
```

Set `HELM_VERSION` to whatever that command returns at implementation time; `v3.19.0` is a starting point, not a verified value.

Next to the other tool install rules:

```make
.PHONY: helm-tool
helm-tool: $(HELM) ## Download helm locally if necessary.
$(HELM): $(LOCALBIN)
	$(call go-install-tool,$(HELM),helm.sh/helm/v3/cmd/helm,$(HELM_VERSION))
```

And under `##@ Build`:

```make
.PHONY: helm
helm: manifests generate kustomize ## Regenerate the Helm chart from config/helm.
	KUSTOMIZE="$(KUSTOMIZE)" ./hack/gen-chart.sh
```

The target is named `helm` and the tool install `helm-tool` because `helm` is the useful name for the thing a human runs.

- [ ] **Step 6: Generate the chart and read the output**

```bash
chmod +x hack/gen-chart.sh
~/gg.sh make helm
find charts/ghostgpu -type f | sort
```

Expected: two files under `crds/`, and one file per object under `templates/generated/` — Namespace, ServiceAccount, ClusterRole(s), ClusterRoleBinding(s), Role, RoleBinding, Service(s), Deployment.

Now read `templates/generated/deployment-ghostgpu-controller-manager.yaml` and confirm the image line reads `{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}` and the namespace reads `{{ .Release.Namespace }}`.

- [ ] **Step 7: Remove the generated Namespace object**

The overlay includes a `Namespace`, which a chart must not create — `helm install --create-namespace` owns that, and a chart that creates its own namespace fails when installed into an existing one.

Add to `hack/gen-chart.sh`, in the loop over `templates`:

```python
    # Helm owns namespace creation through --create-namespace. A chart that
    # ships its own Namespace object cannot be installed into an existing one.
    if doc["kind"] == "Namespace":
        continue
```

Re-run `~/gg.sh make helm` and confirm no `namespace-*.yaml` remains under `templates/generated/`.

- [ ] **Step 8: Confirm the chart renders**

```bash
~/gg.sh make helm-tool
./bin/helm lint charts/ghostgpu
./bin/helm template ghostgpu charts/ghostgpu --namespace ghostgpu-system | head -40
```

Expected: lint passes; the rendered output shows `namespace: ghostgpu-system` and the ghcr image with tag `0.0.0-dev`.

Then confirm the namespace really is dynamic:

```bash
./bin/helm template ghostgpu charts/ghostgpu --namespace some-other-ns | grep -c "namespace: some-other-ns"
./bin/helm template ghostgpu charts/ghostgpu --namespace some-other-ns | grep -c "ghostgpu-system"
```

Expected: the first count is greater than zero, the second is exactly zero. A non-zero second count means a hardcoded namespace leaked through and the chart is broken for every user who does not install into `ghostgpu-system`.

- [ ] **Step 9: Commit**

```bash
git add config/helm hack/gen-chart.sh charts/ Makefile
git commit -s -m "feat: generate a Helm chart from the kustomize manifests

Platform teams compose a test cluster out of charts, so a project installed
by a raw URL is the odd one out in that script. The chart is generated from
the same overlay that produces install.yaml rather than hand-written,
because two hand-maintained copies of the same Deployment and RBAC diverge
silently. CRDs ship in crds/ so a fleet declared in values installs on the
first try."
```

---

### Task 5: Fleets declared in values

**Files:**
- Create: `charts/ghostgpu/templates/fleet.yaml`
- Create: `charts/ghostgpu/values-example.yaml`
- Modify: `hack/gen-chart.sh` (only if Step 3 of Task 4 did not already preserve `templates/fleet.yaml` — confirm it does)

**Interfaces:**
- Consumes: `values.gpuModels` and `values.gpuPools` from Task 4.
- Produces: `GPUModel` and `GPUPool` objects. Task 7 installs with `values-example.yaml` and asserts devices are published.

- [ ] **Step 1: Write the passthrough template**

`charts/ghostgpu/templates/fleet.yaml`:

```yaml
{{- /*
Fleets declared in values are rendered verbatim. `spec` is passed through with
toYaml and nothing here inspects it: the CRD's OpenAPI schema is the validator,
and a values.schema.json describing the same fields would be a second copy of
the API to keep in sync with the first.

The cost is that a typo in spec is reported by the API server at install time
rather than by Helm at render time. Accepted knowingly, in exchange for one
schema instead of two.

Both kinds are cluster-scoped, so nothing here templates a namespace.
*/ -}}
{{- range .Values.gpuModels }}
---
apiVersion: ghostgpu.dev/v1alpha1
kind: GPUModel
metadata:
  name: {{ .name | required "each gpuModels entry needs a name" }}
  labels:
    app.kubernetes.io/managed-by: {{ $.Release.Service }}
    app.kubernetes.io/instance: {{ $.Release.Name }}
spec:
{{ toYaml (.spec | required "each gpuModels entry needs a spec") | indent 2 }}
{{- end }}
{{- range .Values.gpuPools }}
---
apiVersion: ghostgpu.dev/v1alpha1
kind: GPUPool
metadata:
  name: {{ .name | required "each gpuPools entry needs a name" }}
  labels:
    app.kubernetes.io/managed-by: {{ $.Release.Service }}
    app.kubernetes.io/instance: {{ $.Release.Name }}
spec:
{{ toYaml (.spec | required "each gpuPools entry needs a spec") | indent 2 }}
{{- end }}
```

`required` is used on `name` and `spec` because a missing one otherwise renders an object with an empty name, which the API server rejects with an error that says nothing about values.yaml.

- [ ] **Step 2: Write the example values**

`charts/ghostgpu/values-example.yaml`:

```yaml
# A two-node H100 fleet that starts two-thirds full, so a fragmentation
# scenario exists before any workload is submitted.
gpuModels:
  - name: h100
    spec:
      productName: NVIDIA H100 80GB HBM3
      memory: 80Gi
      computeCapability: "9.0"

gpuPools:
  - name: h100-pool
    spec:
      modelRef: h100
      gpusPerNode: 8
      occupancy:
        - busyPerNode: 2
```

- [ ] **Step 3: Confirm the template renders both objects**

```bash
./bin/helm template ghostgpu charts/ghostgpu -f charts/ghostgpu/values-example.yaml \
  | grep -E "^kind: (GPUModel|GPUPool)"
```

Expected: exactly one `kind: GPUModel` and one `kind: GPUPool`.

- [ ] **Step 4: Confirm the spec passes through untouched**

```bash
./bin/helm template ghostgpu charts/ghostgpu -f charts/ghostgpu/values-example.yaml \
  | grep -A 6 "kind: GPUPool"
```

Expected: `modelRef: h100`, `gpusPerNode: 8`, and the `occupancy` list with `busyPerNode: 2`, at the right indentation under `spec:`.

- [ ] **Step 5: Confirm the empty case renders nothing**

```bash
./bin/helm template ghostgpu charts/ghostgpu | grep -cE "^kind: (GPUModel|GPUPool)"
```

Expected: `0`. The default install deploys the operator and no fleet, which is the right default — a chart that invents hardware nobody asked for is a surprise.

- [ ] **Step 6: Confirm a malformed entry fails with a useful message**

```bash
./bin/helm template ghostgpu charts/ghostgpu --set gpuPools[0].spec.gpusPerNode=4
```

Expected: FAIL with `each gpuPools entry needs a name`. If it renders an object with an empty name instead, the `required` guard is not wired up.

- [ ] **Step 7: Confirm regeneration does not delete the hand-written template**

```bash
~/gg.sh make helm
test -f charts/ghostgpu/templates/fleet.yaml && echo "survived"
```

Expected: `survived`. If the file is gone, `hack/gen-chart.sh` is clearing `templates/` rather than `templates/generated/` — fix the script, not the file.

- [ ] **Step 8: Commit**

```bash
git add charts/ghostgpu/templates/fleet.yaml charts/ghostgpu/values-example.yaml
git commit -s -m "feat: declare a simulated fleet in Helm values

What users configure is the fleet, not the operator's replica count, so a
chart exposing only image and resources would be install.yaml with extra
steps. The spec passes through with toYaml and nothing inspects it: the
CRD's schema is the validator, and a values.schema.json would be a second
copy of the API to keep in step with the first."
```

---

### Task 6: CI proves the chart has not drifted

**Files:**
- Create: `.github/workflows/chart.yml`
- Create: `test/chart/equivalence_test.go`

**Interfaces:**
- Consumes: `make helm` from Task 4, `make build-installer` from the existing Makefile.
- Produces: a CI gate. Task 7 adds the live install; this task only compares text.

- [ ] **Step 1: Write the failing equivalence test**

`test/chart/equivalence_test.go`. It runs both generators and compares what they produce, so a CRD or RBAC change that skips the chart cannot merge.

```go
//go:build chart

// Package chart_test asserts that the Helm chart and install.yaml describe the
// same operator. They come from different generators, so nothing but a test
// stops them describing different ones.
package chart_test

import (
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// kindsOf runs a command and returns the "Kind/name" of every document it
// emits, excluding Namespace: Helm creates namespaces through
// --create-namespace rather than as a chart object, so a difference there is
// expected rather than drift.
func kindsOf(t *testing.T, name string, args ...string) map[string]bool {
	t.Helper()

	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("running %s %v: %v", name, args, err)
	}

	kinds := map[string]bool{}
	for _, doc := range strings.Split(string(out), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("parsing a rendered document: %v", err)
		}
		if o.Kind == "" || o.Kind == "Namespace" {
			continue
		}
		kinds[o.Kind+"/"+o.Metadata.Name] = true
	}
	return kinds
}

func TestChartAndInstallerDescribeTheSameObjects(t *testing.T) {
	installer := kindsOf(t, "cat", "dist/install.yaml")
	chart := kindsOf(t, "./bin/helm", "template", "ghostgpu", "charts/ghostgpu",
		"--namespace", "ghostgpu-system")

	for k := range installer {
		if !chart[k] {
			t.Errorf("install.yaml has %s but the chart does not", k)
		}
	}
	for k := range chart {
		if !installer[k] {
			t.Errorf("the chart has %s but install.yaml does not", k)
		}
	}
}
```

- [ ] **Step 2: Run it and confirm it passes against the current tree**

```bash
~/gg.sh make build-installer IMG=ghcr.io/santimillang/ghostgpu:v0.0.0-dev
~/gg.sh go test -tags=chart ./test/chart/ -v
```

Expected: PASS. This test is not red-first — it describes an invariant that already holds, and its job is to fail *later*.

- [ ] **Step 3: Prove the test can fail**

Temporarily delete one object from the chart and confirm the test catches it:

```bash
rm charts/ghostgpu/templates/generated/serviceaccount-ghostgpu-controller-manager.yaml
~/gg.sh go test -tags=chart ./test/chart/ -v
```

Expected: FAIL with `install.yaml has ServiceAccount/ghostgpu-controller-manager but the chart does not`.

Restore it: `~/gg.sh make helm`.

**Do not skip this step.** A drift test that cannot fail is worse than no drift test, because it is a claim of safety rather than a control.

- [ ] **Step 4: Write the workflow**

`.github/workflows/chart.yml`:

```yaml
# The chart is generated from config/helm, so a CRD or RBAC change that skips
# the generator would ship a chart describing a different operator than
# install.yaml does. This job is what makes "generated, not written" true.
name: Chart

on:
  push:
    branches: [main]
  pull_request:

permissions: {}

jobs:
  chart:
    permissions:
      contents: read
    name: chart
    runs-on: ubuntu-latest
    steps:
      - name: Clone the code
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false

      - name: Setup Go
        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod

      - name: Regenerate the chart
        run: make helm

      - name: Fail if the committed chart is stale
        run: |
          if ! git diff --exit-code -- charts/; then
            echo "::error::charts/ is out of date. Run 'make helm' and commit the result."
            exit 1
          fi

      - name: Lint the chart
        run: |
          make helm-tool
          ./bin/helm lint charts/ghostgpu
          ./bin/helm lint charts/ghostgpu -f charts/ghostgpu/values-example.yaml

      - name: Build the installer for comparison
        run: make build-installer IMG=ghcr.io/santimillang/ghostgpu:v0.0.0-dev

      - name: Assert the chart and the installer agree
        run: go test -tags=chart ./test/chart/ -v
```

- [ ] **Step 5: Confirm the whole job passes locally in sequence**

```bash
~/gg.sh make helm && git diff --exit-code -- charts/ && echo "chart is current"
~/gg.sh ./bin/helm lint charts/ghostgpu -f charts/ghostgpu/values-example.yaml
~/gg.sh make build-installer IMG=ghcr.io/santimillang/ghostgpu:v0.0.0-dev
~/gg.sh go test -tags=chart ./test/chart/ -v
```

Expected: every command succeeds. Then discard the `config/manager/kustomization.yaml` change that `build-installer` leaves behind:

```bash
git checkout -- config/manager/kustomization.yaml
```

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/chart.yml test/chart/equivalence_test.go
git commit -s -m "ci: fail when the chart drifts from the installer

The chart and install.yaml come from different generators, so nothing but
a test stops them describing different operators. Regeneration must be a
no-op on a clean tree, and the rendered objects must match. The drift check
was confirmed able to fail by deleting a ServiceAccount before trusting it."
```

---

### Task 7: The chart actually installs

Every check so far is about the chart's text. This one is about whether it works.

**Files:**
- Create: `test/chart/install.sh`
- Modify: `Makefile` (`test-helm` target)
- Modify: `.github/workflows/chart.yml` (a second job)

**Interfaces:**
- Consumes: the chart from Tasks 4 and 5.
- Produces: `make test-helm`.

**Design note:** this deliberately does **not** join the Go e2e suite. That suite deploys the operator with `make deploy` in `BeforeSuite`, and a Helm-installed second operator would be a second controller reconciling the same cluster-scoped `GPUPool`s. Two active reconcilers fighting over one object is a flaky test, not a useful one. A standalone script in its own cluster avoids the problem outright.

- [ ] **Step 1: Write the install script**

`test/chart/install.sh`:

```bash
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
  --set gpuModels[0].spec.productName="NVIDIA H100 80GB HBM3" \
  --set gpuModels[0].spec.memory=80Gi \
  --set gpuModels[0].spec.computeCapability="9.0" \
  --set gpuPools[0].name=chart-pool \
  --set gpuPools[0].spec.modelRef=chart-h100 \
  --set gpuPools[0].spec.gpusPerNode=4 \
  --set gpuPools[0].spec.nodeSelector.chart-test="true" \
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
```

- [ ] **Step 2: Add the Makefile target**

```make
.PHONY: test-helm
test-helm: setup-test-e2e helm helm-tool docker-build ## Install the chart into a live cluster and assert it works.
	$(KIND) load docker-image example.com/ghostgpu:v0.0.1 --name $(KIND_CLUSTER)
	HELM="$(HELM)" ./test/chart/install.sh
	$(MAKE) cleanup-test-e2e
```

`docker-build` uses the default `IMG`, so pass it explicitly:

```make
	$(MAKE) docker-build IMG=example.com/ghostgpu:v0.0.1
```

Replace the `docker-build` prerequisite with that line inside the recipe, before the `kind load`.

- [ ] **Step 3: Run it and confirm it passes**

```bash
chmod +x test/chart/install.sh
printf '{}' > ~/.docker/config.json    # Docker Desktop's credsStore breaks kind
~/gg.sh make test-helm
```

Expected: `PASS: the pool published 4 devices`.

If the pod is `ImagePullBackOff`, the `kind load` did not take or the image reference in values does not match the loaded one — compare `kubectl get deployment -n ghostgpu-chart-test ghostgpu-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}'` against `example.com/ghostgpu:v0.0.1`.

- [ ] **Step 4: Prove the test can fail**

Point the install at an image that does not exist and confirm the script reports failure rather than hanging or passing:

```bash
IMAGE=example.com/does-not-exist ~/gg.sh ./test/chart/install.sh
```

Expected: FAIL, with the deployment's logs or a Helm timeout. Confirm the trap cleaned up: `kubectl get ns ghostgpu-chart-test` should report not found.

- [ ] **Step 5: Add the CI job**

Append to `.github/workflows/chart.yml`:

```yaml
  chart-install:
    permissions:
      contents: read
    name: chart-install
    runs-on: ubuntu-latest
    steps:
      - name: Clone the code
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false

      - name: Setup Go
        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod

      # Pinned deliberately, as in the e2e workflow: this asserts behaviour of
      # real upstream components, so drift must surface as a tracked change.
      - name: Install kind
        run: |
          curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.32.0/kind-linux-$(go env GOARCH)
          chmod +x ./kind
          sudo mv ./kind /usr/local/bin/kind

      - name: Install kwokctl
        run: |
          curl -Lo ./kwokctl https://github.com/kubernetes-sigs/kwok/releases/download/v0.8.0/kwokctl-linux-$(go env GOARCH)
          chmod +x ./kwokctl
          sudo mv ./kwokctl /usr/local/bin/kwokctl

      - name: Install the chart into a live cluster
        run: make test-helm
```

- [ ] **Step 6: Commit**

```bash
git add test/chart/install.sh Makefile .github/workflows/chart.yml
git commit -s -m "test: install the chart into a live cluster

Every other chart check is about its text; only this one is about whether
it works, and without it the chart rots within two PRs. Installed into a
namespace that is deliberately not ghostgpu-system, because the kustomize
overlay hardcodes that name and installing there would pass even if the
namespace substitution had silently failed. Standalone rather than part of
the e2e suite: that suite already deploys an operator, and a second
reconciler for the same cluster-scoped pools is a flaky test."
```

---

### Task 8: Publish the chart on release

**Files:**
- Modify: `.github/workflows/release.yml`
- Create: `.github/workflows/chart-pages.yml`

**Interfaces:**
- Consumes: `charts/ghostgpu` from Tasks 4 and 5.
- Produces: `oci://ghcr.io/santimillang/charts/ghostgpu` and a `gh-pages` Helm repository index.

- [ ] **Step 1: Add chart packaging and OCI push to the release workflow**

After the GoReleaser step in `.github/workflows/release.yml`:

```yaml
      # The chart's version and the operator it deploys are both the tag: one
      # release, one number, nothing for a user to correlate.
      - name: Package and push the chart
        run: |
          VERSION="${{ steps.version.outputs.version }}"
          SEMVER="${VERSION#v}"

          make helm helm-tool
          ./bin/helm package charts/ghostgpu \
            --version "$SEMVER" --app-version "$VERSION" \
            --destination dist/

          if [ "${{ github.event_name }}" = "push" ]; then
            ./bin/helm push "dist/ghostgpu-${SEMVER}.tgz" \
              oci://${{ env.REGISTRY }}/${{ github.repository_owner }}/charts
          else
            echo "dry run: packaged but not pushed"
          fi
```

`helm push` reuses the registry login the workflow already performs for the image.

- [ ] **Step 2: Attach the packaged chart to the release**

Extend the `files:` list of the existing "Publish the release" step:

```yaml
        with:
          files: |
            dist/install.yaml
            dist/ghostgpu-*.tgz
```

- [ ] **Step 3: Add the chart install line to the release notes**

In the same step's `body:`, after the `kubectl apply` block:

````yaml
            Or with Helm:

            ```sh
            helm install ghostgpu oci://ghcr.io/${{ github.repository_owner }}/charts/ghostgpu \
              --version ${{ steps.version.outputs.version }} \
              --namespace ghostgpu-system --create-namespace
            ```
````

Note the `--version` value: `helm` accepts the `v` prefix on an OCI pull, but the packaged chart version has it stripped. Verify this on the first real release and correct the note if `helm` rejects it.

- [ ] **Step 4: Write the gh-pages index workflow**

`.github/workflows/chart-pages.yml`:

```yaml
# A classic Helm repository alongside the OCI one. OCI is the modern path, but
# plenty of tooling and plenty of humans still reach for `helm repo add`, and
# being absent from that path is a complaint we can avoid for the cost of an
# index file.
name: Chart Pages

on:
  release:
    types: [published]
  workflow_dispatch:

permissions: {}

jobs:
  index:
    permissions:
      contents: write
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 0

      - name: Publish the chart to the gh-pages index
        uses: helm/chart-releaser-action@v1
        with:
          charts_dir: charts
        env:
          CR_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

`chart-releaser-action` packages every chart under `charts/`, creates a release per chart version, and maintains `index.yaml` on `gh-pages`.

- [ ] **Step 5: Enable GitHub Pages for the `gh-pages` branch**

This is a repository setting, not a file. In Settings → Pages, set the source to the `gh-pages` branch, root directory. The branch will not exist until the workflow runs once — run it with `workflow_dispatch` first, then set the source.

- [ ] **Step 6: Run the dry release and inspect**

```bash
gh.exe workflow run release.yml --repo santimillang/ghostgpu -f version=v0.0.0-dryrun2
gh.exe run watch --repo santimillang/ghostgpu
```

Expected: green, with `dry run: packaged but not pushed` in the log and `ghostgpu-0.0.0-dryrun2.tgz` among the uploaded artifacts.

- [ ] **Step 7: Commit**

```bash
git add .github/workflows/release.yml .github/workflows/chart-pages.yml
git commit -s -m "ci: publish the Helm chart on release

Both an OCI chart on ghcr and a classic gh-pages index: OCI is the modern
path and rides the registry the image already uses, but a good share of
tooling and of people still reach for `helm repo add`, and being missing
from that path costs a user for the price of an index file. The chart
version and the operator it deploys are both the tag, so there is nothing
for a user to correlate."
```

---

### Task 9: Sign and attest

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `.goreleaser.yaml` (SBOM for the archives)

**Interfaces:**
- Consumes: the image and archives from earlier tasks.
- Produces: cosign signatures, SLSA provenance attestations, SBOMs, and a documented verification command.

- [ ] **Step 1: Capture the image digest**

Signing must target a digest, not a tag: a tag can move, and a signature over a mutable reference proves nothing. Add `id: build` to the existing "Build and push the manager image" step so its digest output is addressable.

- [ ] **Step 2: Add signing and attestation to the release workflow**

After the image build step:

```yaml
      - name: Install cosign
        uses: sigstore/cosign-installer@v3

      # Keyless: the signature is bound to this workflow's OIDC identity, so
      # there is no key to store, rotate, or leak.
      - name: Sign the image
        run: |
          cosign sign --yes \
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}

      - name: Attest the image build provenance
        uses: actions/attest-build-provenance@v2
        with:
          subject-name: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          subject-digest: ${{ steps.build.outputs.digest }}
          push-to-registry: true
```

Extend `permissions:` at the top of the workflow with `id-token: write` and `attestations: write`. Without `id-token: write`, keyless signing fails with an OIDC error that does not name the missing permission.

- [ ] **Step 3: Attest the release archives**

After the GoReleaser step:

```yaml
      - name: Attest the CLI archives
        if: github.event_name == 'push'
        uses: actions/attest-build-provenance@v2
        with:
          subject-path: "dist/*.tar.gz,dist/*.zip"
```

- [ ] **Step 4: Add SBOMs for the archives**

In `.goreleaser.yaml`:

```yaml
sboms:
  - artifacts: archive
```

GoReleaser invokes syft, which the action's runner image provides.

- [ ] **Step 5: Add an SBOM to the image**

In the "Build and push the manager image" step:

```yaml
        with:
          sbom: true
          provenance: mode=max
```

- [ ] **Step 6: Verify the signature in CI**

A signing story nobody has verified is a claim, not a control. After the signing step:

```yaml
      - name: Verify the signature we just made
        run: |
          cosign verify \
            --certificate-identity-regexp "^https://github.com/${{ github.repository }}/.github/workflows/release.yml@.*" \
            --certificate-oidc-issuer https://token.actions.githubusercontent.com \
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}
```

- [ ] **Step 7: Publish the verification command in the release notes**

Add to the release body, after the install instructions:

````yaml
            ## Verify

            ```sh
            cosign verify \
              --certificate-identity-regexp "^https://github.com/${{ github.repository }}/.github/workflows/release.yml@.*" \
              --certificate-oidc-issuer https://token.actions.githubusercontent.com \
              ghcr.io/${{ github.repository }}:${{ steps.version.outputs.version }}
            ```
````

- [ ] **Step 8: Run the dry release and confirm every step is green**

```bash
gh.exe workflow run release.yml --repo santimillang/ghostgpu -f version=v0.0.0-dryrun3
gh.exe run watch --repo santimillang/ghostgpu
```

Note that a `workflow_dispatch` run **does** push the image (the build step has no event guard), so the signature and verification are exercised for real. That is intentional: it is the only way the dry run rehearses signing.

- [ ] **Step 9: Commit**

```bash
git add .github/workflows/release.yml .goreleaser.yaml
git commit -s -m "ci: sign and attest the release artifacts

A platform team that runs admission policy asks for this before it asks
about features, and keyless signing costs a step and no key management.
The signature targets the digest rather than the tag, because a signature
over a mutable reference proves nothing, and CI verifies the signature it
just made: an unverified signing story is a claim rather than a control."
```

---

### Task 10: Document the paths that now exist

**Files:**
- Create: `charts/ghostgpu/README.md`
- Modify: `README.md` (Install and Quickstart sections only)
- Modify: `CONTRIBUTING.md` (a release runbook)
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing code depends on. The full README rewrite is separate work and is deliberately not attempted here.

- [ ] **Step 1: Write the chart README**

`charts/ghostgpu/README.md` covering: install with OCI and with `helm repo add`; the `values.yaml` reference (image, gpuModels, gpuPools); a worked `values-example.yaml`; and — prominently — the CRD upgrade wart:

```markdown
## Upgrading

Helm does not update CRDs in a chart's `crds/` directory on `helm upgrade`.
When a release changes the ghostgpu API, apply the CRDs by hand first:

```sh
kubectl apply -f https://github.com/santimillang/ghostgpu/releases/latest/download/install.yaml \
  --selector app.kubernetes.io/name=ghostgpu
```

They live in `crds/` rather than `templates/` because Helm resolves the whole
release's resource kinds before creating anything, so a `GPUPool` declared in
values alongside its own CRD would fail on first install.
```

Verify that `kubectl apply --selector` line actually selects only the CRDs from `install.yaml` before publishing it. If the CRDs do not carry that label, replace the command with one that works rather than shipping one that does not.

- [ ] **Step 2: Fix the README's Install section**

Replace the single `kubectl apply` with the three paths that now exist — Helm, `install.yaml`, and the CLI:

````markdown
## Install

Helm:

```sh
helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --namespace ghostgpu-system --create-namespace
```

Or a single manifest:

```sh
kubectl apply -f https://github.com/santimillang/ghostgpu/releases/latest/download/install.yaml
```

The CLI is a separate binary — download it from
[the latest release](https://github.com/santimillang/ghostgpu/releases/latest),
or build it with `make build-cli`.
````

- [ ] **Step 3: Fix the Quickstart's step 3**

It currently says `make build-cli`, which requires a clone the rest of the quickstart does not. Replace with a download:

```sh
# 3. Give your kwok nodes some GPUs
curl -sSfL https://github.com/santimillang/ghostgpu/releases/latest/download/ghostgpu_Linux_x86_64.tar.gz \
  | tar xz ghostgpu
./ghostgpu up --gpus-per-node 8 --nvlink-domain-size 4
```

**Verify the archive name against a real release before committing this.** GoReleaser's default name template may render `Linux_x86_64` or `linux_amd64` depending on version; a curl line with the wrong filename is exactly the broken instruction this task exists to remove. Check with `gh.exe release view --repo santimillang/ghostgpu`, or against the dry-run artifacts from Task 3.

- [ ] **Step 4: Add a release runbook to CONTRIBUTING.md**

```markdown
## Cutting a release

1. Run the release workflow with `workflow_dispatch` and a throwaway version
   (`v0.0.0-dryrun`). It builds everything and publishes nothing.
2. Confirm the artifacts: `install.yaml`, five CLI archives, `checksums.txt`,
   and a packaged chart.
3. Move the `CHANGELOG.md` entries out of `Unreleased` under the new version.
4. Tag and push: `git tag -s v0.1.0 && git push origin v0.1.0`.
5. **Set the ghcr package visibility to public** for both
   `ghostgpu` and `charts/ghostgpu`. Package visibility is independent of
   repository visibility and both default to private, so an anonymous
   `docker pull` or `helm pull` fails with a bare `denied` until this is done.
   It is invisible from inside the repository and is the likeliest first-user
   complaint.
6. Verify as a stranger would: pull the image, install the chart, and download
   and run the CLI, all from a machine with no clone of this repository.

A published tag cannot be replaced. If a release is wrong, bump the patch
version rather than moving the tag: the image tag is immutable, so a re-run
would fail on push and leave the release half-made.
```

- [ ] **Step 5: Add the CHANGELOG entries**

Under `## [Unreleased]` → `### Added`, entries for the CLI binaries, the Helm chart, `ghostgpu version`, and signing — each explaining *why*, matching the existing entries' voice.

- [ ] **Step 6: Verify every command in what you just wrote**

Run each command in the README Install section, the chart README, and the CONTRIBUTING runbook. A broken instruction in a document written to fix broken instructions is the specific failure this task must not produce.

- [ ] **Step 7: Commit**

```bash
git add charts/ghostgpu/README.md README.md CONTRIBUTING.md CHANGELOG.md
git commit -s -m "docs: document the install paths that now exist

The quickstart told people to install from a release URL and then run
make build-cli, which cannot be followed without a clone. It now points at
a downloadable binary. The release runbook records the ghcr package
visibility step, which is invisible from inside the repository and is the
likeliest way a first user meets a bare 'denied'."
```

---

## Self-review

**Spec coverage.** Every section of the spec maps to a task: GoReleaser → 3; chart generation and sentinels → 4; `crds/` vs `templates/` → 1 (verify) and 4 (implement); values contract → 4 and 5; drift defense → 6; live install → 7; publishing both OCI and gh-pages → 8; supply chain → 9; the ghcr visibility trap, the partial-publication rule, and the dry-run gate → 10's runbook, with the dry run itself exercised in 3, 8, and 9. `ghostgpu version` → 2.

**Known deviation from the spec.** The spec says the live Helm install is an "e2e job". This plan makes it a standalone `make test-helm` instead, because the Go e2e suite deploys an operator in `BeforeSuite` and a Helm-installed second operator would be a competing reconciler for the same cluster-scoped pools. The intent — a live install asserted by CI — is unchanged.

**Ordering dependency.** Task 1 gates Tasks 4 and 5. If the spike shows templated CRDs work, the spec needs amending before either starts.
