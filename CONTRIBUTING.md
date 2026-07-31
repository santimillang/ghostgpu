# Contributing to ghostgpu

Thanks for your interest in ghostgpu. This document covers how to get a development environment running, what we expect from a change, and how to get it merged.

By participating you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

## Developer Certificate of Origin (DCO)

All commits must be signed off. The sign-off certifies that you wrote the patch or otherwise have the right to submit it under the project's license — see [developercertificate.org](https://developercertificate.org/).

```sh
git commit -s -m "feat: add the thing"
```

This appends a `Signed-off-by:` trailer using your `user.name` and `user.email`. If you forget on your most recent commit:

```sh
git commit --amend -s --no-edit
```

We use DCO rather than a CLA deliberately: it is lighter weight for contributors and is what CNCF projects require.

## Development environment

**ghostgpu must be developed on Linux, or on Windows via WSL2.** This is not a preference:

- `kubebuilder` publishes no Windows binary.
- `envtest` requires a Linux `kube-apiserver`; Kubernetes does not build one for Windows.

You will need:

| Tool | Version | Why |
|---|---|---|
| Go | 1.26.5 | matches `go.mod` |
| Docker | any recent | backs `kind` |
| `kubebuilder` | v4.15.0 | scaffolding |
| `kind` | v0.32.0 | e2e clusters |
| `kwokctl` | v0.8.0 | simulated nodes |

Versions are pinned deliberately. e2e tests assert the behavior of real upstream components, so drift must surface as a tracked change rather than silent rot.

```sh
make manifests generate   # regenerate CRDs and deepcopy code
make build                # build the manager
make test                 # unit tests + envtest
make lint                 # golangci-lint
make test-e2e             # e2e against kwok + kind
```

`make test` downloads its own envtest binaries into `bin/` on first run.

## Making a change

1. **Open an issue first** for anything non-trivial. It is cheaper to disagree about an approach in an issue than in a finished PR.
2. **Branch from `main`.** Use a descriptive prefix: `feat/`, `fix/`, `docs/`, `chore/`, `refactor/`, `test/`.
3. **Write the test first.** ghostgpu is a testing tool; it would be embarrassing for it to be poorly tested. See "Testing philosophy" below.
4. **Keep commits focused.** One logical change per commit, with a message explaining *why*, not just *what*.
5. **Open a pull request.** Fill in the template. CI must be green before review.

### Commit messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add MIG profile expansion to the topology controller

Longer explanation of why this change exists, what alternative was
rejected, and anything a future reader would find surprising.

Signed-off-by: Your Name <you@example.com>
```

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `perf`, `ci`.

## Testing philosophy

ghostgpu has three test layers, and putting a test in the wrong one makes it either slow or useless:

- **Unit tests** (`go test`, fake client) — pure logic: device identity, ResourceSlice construction, label formatting, safety checks. Fast, no cluster.
- **envtest** (`internal/controller`) — behavior that needs a real API server: CRD schema installation, defaulting, validation rejection. **envtest runs no kube-scheduler**, so it cannot assert scheduling decisions.
- **e2e** (`test/e2e`, kind + kwok) — the only place scheduling *decisions* can be asserted, because it runs a real `kube-scheduler`.

If your change affects what the scheduler does, it needs an e2e test. If it affects only what objects we write, envtest or a unit test is correct and much faster.

## The safety invariant

ghostgpu modifies `Node` objects. It must **never** touch a node that is not kwok-managed — doing so would mean mutating real infrastructure.

Every write path goes through `safety.IsSimulatedNode`. `TestReconcileRefusesRealNodes` guards this. **Do not weaken or skip that test.** If you need to change this behavior, say so explicitly in your PR description so it gets scrutiny.

## Fidelity claims

ghostgpu is only as valuable as its fidelity claims are honest. When you add a simulated behavior, classify it in the fidelity contract:

- **Faithful** — byte-comparable to the real thing (object shapes, DCGM metric names and label sets, GFD node labels).
- **Approximated** — plausible but not measured from hardware (metric value curves, phase timings).
- **Not simulated** — explicitly out of scope (CUDA execution, interconnect bandwidth, driver internals).

Do not silently upgrade something from "approximated" to "faithful" in the docs without evidence.

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

## Reporting bugs and requesting features

Use the issue templates. For security vulnerabilities, **do not open a public issue** — see [SECURITY.md](SECURITY.md).
