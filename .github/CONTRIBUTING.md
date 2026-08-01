# Contributing to ghostgpu

Thanks for your interest. This covers getting a development environment running, what a good change looks like, and how releases are cut.

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Development environment

**ghostgpu must be developed on Linux, or on Windows via WSL2.** Not a preference: `kubebuilder` publishes no Windows binary, and `envtest` needs a Linux `kube-apiserver`, which Kubernetes does not build for Windows.

| Tool | Version |
|---|---|
| Go | 1.26.5 (matches `go.mod`) |
| Docker | any recent (backs `kind`) |
| `kubebuilder` | v4.15.0 |
| `kind` | v0.32.0 |
| `kwokctl` | v0.8.0 |

Versions are pinned deliberately. e2e tests assert the behaviour of real upstream components, so drift must surface as a tracked change rather than silent rot.

```sh
make manifests generate   # regenerate CRDs and deepcopy code
make build                # build the manager
make test                 # unit tests + envtest
make lint                 # golangci-lint
make test-e2e             # e2e against kwok + kind
make helm                 # regenerate the Helm chart from config/helm
```

`make test` downloads its own envtest binaries into `bin/` on first run.

## Making a change

Open an issue first for anything non-trivial — it is cheaper to disagree about an approach in an issue than in a finished PR. Branch from `main`, write the test first, keep commits focused, and explain *why* in the commit body. Commits use [Conventional Commits](https://www.conventionalcommits.org/) and must be signed off (`git commit -s`) under the [DCO](https://developercertificate.org/). CI must be green before review.

## Testing philosophy

Three layers, and putting a test in the wrong one makes it either slow or useless:

- **Unit tests** (fake client) — pure logic: device identity, ResourceSlice construction, label formatting, safety checks. Fast, no cluster.
- **envtest** (`internal/controller`) — behaviour needing a real API server: CRD schemas, defaulting, validation rejection. **envtest runs no kube-scheduler**, so it cannot assert scheduling decisions.
- **e2e** (`test/e2e`, kind + kwok) — the only place scheduling *decisions* can be asserted, because it runs a real `kube-scheduler`.

If your change affects what the scheduler does, it needs an e2e test. If it affects only what objects we write, envtest or a unit test is correct and much faster.

The same rule applies to the Helm chart: `make test-helm` installs it into a live cluster, because rendering and linting both pass on a chart that cannot actually install.

## The safety invariant

ghostgpu modifies `Node` objects and must **never** touch one that is not kwok-managed — that would mean mutating real infrastructure. Every write path goes through `safety.IsSimulatedNode`, guarded by `TestReconcileRefusesRealNodes`. **Do not weaken or skip that test.** If you need to change this behaviour, say so explicitly in the PR description so it gets scrutiny.

## Fidelity claims

ghostgpu is only as valuable as its fidelity claims are honest. Classify any simulated behaviour you add:

- **Faithful** — byte-comparable to the real thing (object shapes, DCGM metric names and label sets, GFD node labels).
- **Approximated** — plausible but not measured from hardware (metric value curves).
- **Not simulated** — explicitly out of scope (CUDA execution, interconnect bandwidth, driver internals).

Do not silently upgrade something from "approximated" to "faithful" without evidence.

## Cutting a release

1. Run the release workflow with `workflow_dispatch` and a throwaway version (`v0.0.0-dryrun`) to rehearse it. It **does** push and sign the image, and verifies that signature — an untested signing story is a claim, not a control. It packages the chart without pushing it, does not move the `latest` tag, and creates no GitHub Release; the archives, `install.yaml`, and the chart are attached to the run as artifacts instead.
2. Confirm those artifacts look right.
3. Move the `CHANGELOG.md` entries out of `Unreleased` under the new version.
4. Tag and push: `git tag -s v0.1.0 && git push origin v0.1.0`.
5. **Set the ghcr package visibility to public** for both `ghostgpu` and `charts/ghostgpu`. Package visibility is independent of repository visibility and both default to private, so an anonymous `docker pull` or `helm pull` fails with a bare `denied` until this is done. It is invisible from inside the repository and is the likeliest first-user complaint.
6. Verify as a stranger would: pull the image, install the chart, and download and run the CLI, from a machine with no clone of this repository.

If a release is wrong, **bump the patch version rather than moving the tag.** ghcr tags are mutable, so re-running would overwrite the image while leaving the GitHub Release, its assets, and the published chart version inconsistent — and a cosign signature for the old digest is already in a public transparency log that cannot be rewritten.

## Reporting bugs and requesting features

Use the issue templates. For security vulnerabilities, **do not open a public issue** — see [SECURITY.md](SECURITY.md).
