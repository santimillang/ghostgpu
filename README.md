# ghostgpu

[![Tests](https://github.com/santimillang/ghostgpu/actions/workflows/test.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/test.yml)
[![Lint](https://github.com/santimillang/ghostgpu/actions/workflows/lint.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/lint.yml)
[![E2E](https://github.com/santimillang/ghostgpu/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/test-e2e.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/santimillang/ghostgpu)](https://goreportcard.com/report/github.com/santimillang/ghostgpu)
[![Go Reference](https://pkg.go.dev/badge/github.com/santimillang/ghostgpu.svg)](https://pkg.go.dev/github.com/santimillang/ghostgpu)
[![Go Version](https://img.shields.io/github/go-mod/go-version/santimillang/ghostgpu)](go.mod)
[![Release](https://img.shields.io/github/v/release/santimillang/ghostgpu?include_prereleases&sort=semver&label=release)](https://github.com/santimillang/ghostgpu/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![DCO](https://img.shields.io/badge/DCO-required-brightgreen.svg)](https://developercertificate.org/)

Simulate GPU clusters on Kubernetes. Test GPU-aware schedulers, autoscalers, and platform tooling with **zero GPU hardware**.

ghostgpu builds on [kwok](https://kwok.sigs.k8s.io/) and publishes Dynamic Resource Allocation (DRA) `ResourceSlice`s plus legacy extended-resource capacity, so a real `kube-scheduler` makes real placement decisions against hardware that does not exist.

> **Status:** early development. The v0.1 core works and is covered end-to-end against a real `kube-scheduler`, but nothing is released yet and the `v1alpha1` API carries no compatibility guarantee. Build from source.

## Why

Testing GPU scheduling logic normally requires GPUs — expensive to idle, slow to provision, and impractical in CI. ghostgpu lets you run the real thing (Kueue, Volcano, KEDA, your own operator) against simulated fleets on a laptop.

Already verified against a real `kube-scheduler`: pods are placed against simulated GPU capacity and correctly refused once that capacity is exhausted — including MIG-style partitioning, where overlapping profiles on the same physical GPU are mutually exclusive.

## Quickstart

Needs [`kwokctl`](https://kwok.sigs.k8s.io/docs/user/installation/), `kind`, Docker, and Go.

```sh
# 1. A kwok cluster with DRA enabled
kwokctl create cluster --name ghostgpu --runtime kind \
  --kube-feature-gates "DynamicResourceAllocation=true" \
  --kube-runtime-config "resource.k8s.io/v1=true"

# 2. Install the CRDs and run the operator
make install
make run

# 3. Give your kwok nodes some GPUs
make build-cli
./bin/ghostgpu up --gpus-per-node 8 --nvlink-domain-size 4
```

```
gpumodel/h100 created
gpupool/h100-pool created
simulating 16 GPUs across 2 nodes
```

Each node now advertises `nvidia.com/gpu: 8`, GPU Feature Discovery labels, and a DRA `ResourceSlice` whose devices carry product, UUID, and NVLink-domain attributes. Pods scheduling against them are placed by the real scheduler, and refused when the simulated capacity runs out.

`--dry-run` prints the manifests instead of applying them, and contacts no cluster:

```sh
./bin/ghostgpu up --gpu NVIDIA-A100-SXM4-40GB --memory 40Gi \
  --compute-capability 8.0 --dry-run | kubectl apply -f -
```

### MIG

Partition each GPU into MIG instances and let the scheduler enforce that overlapping profiles on one card are mutually exclusive:

```sh
./bin/ghostgpu up --gpu NVIDIA-H100-80GB-HBM3 --sharing-mode mig --gpus-per-node 16
```

Profiles come from built-in tables matching NVIDIA's published instance counts, so an H100 offers seven `1g.10gb` per card but only four `1g.20gb` — memory binds before compute slices do. `--mig-profiles 1g.10gb,3g.40gb` restricts a pool to a subset.

Exclusivity is enforced by the upstream scheduler through DRA shared counters; ghostgpu contributes no allocation logic. **On the DRA path this is faithful.** The legacy extended-resource projection (`nvidia.com/mig-1g.10gb` and friends, NVIDIA's `mixed` strategy) cannot express exclusivity, because scalar resources have no way to say "these two are the same silicon" — see the fidelity contract in the design spec, and [#25](https://github.com/santimillang/ghostgpu/issues/25).

ghostgpu only ever modifies nodes carrying kwok's `kwok.x-k8s.io/node` annotation. A node without it is never touched, whatever the pool selector matches — see [SECURITY.md](SECURITY.md).

## Capabilities

| Area | Status |
|---|---|
| DRA `ResourceSlice` publication + legacy `nvidia.com/gpu` capacity + GFD labels | working, unreleased (v0.1) |
| MIG / partitionable devices | working, unreleased (v0.2) |
| DCGM-shaped Prometheus metrics | planned (v0.3) |
| Behavioral workload simulation (weight-download → warmup → training) | planned (v0.4) |
| GPU fault injection (XID, ECC, thermal, device loss) | planned (v0.5) |

## Prior art

[`fake-gpu-operator`](https://github.com/run-ai/fake-gpu-operator) is an actively maintained project covering capacity advertising, dynamic GPU-utilization metrics, and basic DRA on kwok. If that is all you need, use it.

ghostgpu differentiates on **MIG-instance fidelity**, **fault injection**, and **behavioral workload simulation** — none of which `fake-gpu-operator` currently provides.

## Development

Requires Linux (or WSL2 on Windows): `kubebuilder` ships no Windows binary, and `envtest` needs a Linux `kube-apiserver`.

```sh
make manifests generate   # regenerate CRDs and deepcopy code
make build                # build the manager
make build-cli            # build the ghostgpu CLI
make test                 # unit tests + envtest
make test-e2e             # e2e against kwok + kind
```

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, testing layers, and PR process.

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Governance](GOVERNANCE.md) · [Maintainers](MAINTAINERS.md)
- [Security policy and threat model](SECURITY.md)
- [Changelog](CHANGELOG.md)

## License

Apache-2.0 — see [LICENSE](LICENSE). Contributions require [DCO](https://developercertificate.org/) sign-off (`git commit -s`).
