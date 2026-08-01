# Spike: does Helm install a CRD before a CR of that kind in the same release?

Date: 2026-07-31

## Question

The distribution spec's chart layout assumes Helm resolves REST mappings for
every rendered manifest *before* creating anything in a release, so a CRD and
a custom resource of that kind cannot both live in `templates/` — the CR's
apply would fail with "no matches for kind" because the CRD (also pending in
the same release) hasn't registered with the API server yet. If that
assumption is wrong, CRDs could live in `templates/` (where `helm upgrade`
would update them), which is strictly better than the `crds/` special
directory Helm normally recommends.

## Method

`hack/spikes/helm-crd-order.sh` builds two throwaway charts against a live
cluster, using a CRD private to the spike (`widgets.spike.ghostgpu.dev`, not
ghostgpu's own CRDs):

- **Chart B (control):** CRD in `crds/`, CR in `templates/` — the layout the
  spec's design assumes works.
- **Chart A (subject):** CRD and CR both in `templates/` — the layout under
  suspicion.

The control runs first and the script aborts if it fails, because a result
from the subject is uninterpretable if the install mechanism itself is
broken. (A previous spike in this repo reported a false "eviction does not
work" when the real cause was an invalid CEL field in its own control path —
this script exists to not repeat that mistake.)

## Environment

- Helm: `v3.19.0` (installed via `go install helm.sh/helm/v3/cmd/helm@v3.19.0`
  into `$(go env GOPATH)/bin`; `helm version` reports `Version:"v3.19"`,
  `GitCommit` empty because the `go install` build doesn't stamp ldflags —
  the binary is still built from the `v3.19.0` tag)
- Kubernetes: client `v1.36.1`, server `v1.36.1` (kwok+kind cluster,
  `kwok-ghostgpu-test-e2e-control-plane` node, containerd 2.3.1)

## Results (verbatim)

```
== control: CRD in crds/, CR in templates/ ==
NAME: crdorder-b
LAST DEPLOYED: Fri Jul 31 17:34:32 2026
NAMESPACE: default
STATUS: deployed
REVISION: 1
TEST SUITE: None
widget.spike.ghostgpu.dev/spike-widget
release "crdorder-b" uninstalled
customresourcedefinition.apiextensions.k8s.io "widgets.spike.ghostgpu.dev" deleted

== subject: CRD and CR both in templates/ ==
Error: INSTALLATION FAILED: unable to build kubernetes objects from release manifest: resource mapping not found for name: "spike-widget" namespace: "" from "": no matches for kind "Widget" in version "spike.ghostgpu.dev/v1"
ensure CRDs are installed first
RESULT: templated CRDs fail alongside their CRs, as the spec assumes.
```

The control installed cleanly and `kubectl get widget spike-widget -o name`
returned `widget.spike.ghostgpu.dev/spike-widget`, confirming the CRD from
`crds/` was registered and the CR from `templates/` was accepted in the same
release. The control was then uninstalled and its CRD deleted before the
subject ran, so the two charts did not interfere with each other.

The subject failed exactly as predicted: Helm rejected the CR with
`no matches for kind "Widget" in version "spike.ghostgpu.dev/v1"` —
`ensure CRDs are installed first`. The whole release was rejected atomically
(Helm builds/validates all objects in the manifest before applying any of
them), so no partial resources were left on the cluster.

## Verdict

**Confirmed: the spec's premise holds.** Helm does not install a CRD ahead of
a CR of that kind when both are rendered from `templates/` in the same
release — the release fails outright with "no matches for kind". The control
ran clean, so this is a real result, not an artifact of a broken control
path.

**Conclusion for the chart design:** ghostgpu's CRDs must go in the special
`crds/` directory, not `templates/`, if any chart also installs a CR of that
kind in the same release. This is the layout the distribution spec already
assumes — no revision needed. The known tradeoff of `crds/` stands: Helm
does not update or delete CRDs placed there on `helm upgrade`/`helm
uninstall` (by design, to avoid destructive changes to installed CRs), so
CRD schema changes across ghostgpu versions will need a separate upgrade
path (documented as a concern for later tasks, not solved by this spike).

## Surprises / notes

- Output ordering from the script was unreliable when captured through
  `wsl.exe -e bash -lc '...' 2>&1` directly in a terminal — stdout (script's
  own `echo`s) and stderr (Helm's error output) interleaved out of order
  because of differing buffering across the `wsl.exe` → `bash` → `helm`
  process chain. Redirecting the script's combined output to a file inside
  WSL (`... > /tmp/spike-run.log 2>&1`) and reading the file back produced
  the correct, verbatim order. Anyone rerunning this spike should redirect
  to a file rather than trusting a live terminal capture.
- `go install helm.sh/helm/v3/cmd/helm@v3.19.0` works fine as an install
  method and lands the binary in `$(go env GOPATH)/bin` (`/home/santi/go/bin`
  here), already on `PATH` — no separate download/tarball step was needed.
- The overall release failure is atomic: Helm validates/builds the full set
  of objects before applying any, so the subject's failed install left zero
  resources on the cluster (confirmed no leftover `widgets.spike.ghostgpu.dev`
  CRD or CR needed manual cleanup beyond the script's own trap).
