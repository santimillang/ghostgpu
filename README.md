<img src="docs/assets/ghostgpu.svg" width="88" alt="">

# ghostgpu

[![Tests](https://github.com/santimillang/ghostgpu/actions/workflows/test.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/santimillang/ghostgpu?include_prereleases&sort=semver&label=release)](https://github.com/santimillang/ghostgpu/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Simulate GPU clusters on Kubernetes. Test GPU-aware schedulers, autoscalers, and platform tooling with **zero GPU hardware**.

ghostgpu builds on [kwok](https://kwok.sigs.k8s.io/) and publishes Dynamic Resource Allocation (DRA) `ResourceSlice`s plus legacy extended-resource capacity, so a real `kube-scheduler` makes real placement decisions against hardware that does not exist.

**📖 [Documentation](https://santimillang.github.io/ghostgpu/)**

> **Status:** early development. The core works and is covered end-to-end against a real `kube-scheduler`, but the `v1alpha1` API carries no compatibility guarantee yet.

## Why

Testing GPU scheduling logic normally requires GPUs — expensive to idle, slow to provision, and impractical in CI. ghostgpu lets you run the real thing (Kueue, Volcano, KEDA, your own operator) against simulated fleets on a laptop, including a copy of the fleet you actually run, read out of your own cluster with `ghostgpu capture`.

Already verified against a real `kube-scheduler`: pods are placed against simulated GPU capacity and correctly refused once that capacity is exhausted — including MIG-style partitioning, where overlapping profiles on the same physical GPU are mutually exclusive.

## Quickstart

```sh
# 1. A kwok cluster with DRA enabled
kwokctl create cluster --name ghostgpu --runtime kind \
  --kube-feature-gates "DynamicResourceAllocation=true" \
  --kube-runtime-config "resource.k8s.io/v1=true"

# 2. Install ghostgpu
helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --namespace ghostgpu-system --create-namespace

# 3. Get the CLI
curl -sSfL https://github.com/santimillang/ghostgpu/releases/latest/download/ghostgpu_linux_amd64.tar.gz \
  | tar xz ghostgpu

# 4. Give your kwok nodes some GPUs
./ghostgpu up --gpus-per-node 8 --nvlink-domain-size 4
```

```
gpumodel/h100 created
gpupool/h100-pool created
simulating 16 GPUs across 2 nodes
```

Each node now advertises `nvidia.com/gpu: 8`, GPU Feature Discovery labels, and a DRA `ResourceSlice` whose devices carry product, UUID, and NVLink-domain attributes. Pods scheduling against them are placed by the real scheduler, and refused when the simulated capacity runs out.

`kubectl apply -f https://github.com/santimillang/ghostgpu/releases/latest/download/install.yaml` works too, if you would rather not use Helm. Installing is safe alongside real hardware: ghostgpu only ever modifies nodes carrying kwok's `kwok.x-k8s.io/node` annotation.

→ [Full getting-started guide](https://santimillang.github.io/ghostgpu/getting-started/)

## Scenarios

[`examples/`](examples) holds worked scenarios, each answering a question you might need to ask of your own tooling. **All are applied and checked by CI**, so a scenario that stops working fails the build.

| Scenario | The question it asks |
|---|---|
| [Fragmented fleet](examples/fragmented-fleet) | Seven GPUs free, spread 2/2/2/1 — does my four-GPU job schedule? Should it? |
| [GPU failure](examples/gpu-failure) | A card dies under a running job. Does my remediation drain it? Does the job come back? |
| [MIG exclusivity](examples/mig-exclusivity) | Two profiles overlap on one card. Does the scheduler refuse the second? |
| [Idle reclamation](examples/idle-reclamation) | A notebook squats at 4% beside a trainer at 90%. Does my tool pick the right one? |

## What it simulates

| Area | Status |
|---|---|
| DRA `ResourceSlice` publication, `nvidia.com/gpu` capacity, GFD labels | working |
| [MIG / partitionable devices](https://santimillang.github.io/ghostgpu/simulating/mig/) | working |
| [Pre-existing occupancy and fragmentation](https://santimillang.github.io/ghostgpu/simulating/occupancy/) | working |
| [DCGM-shaped metrics with per-pod attribution](https://santimillang.github.io/ghostgpu/simulating/metrics/) | working |
| [Fault injection](https://santimillang.github.io/ghostgpu/simulating/faults/) — XID, device loss, drain-before-reboot | working |
| Behavioural phase timeline | deferred, [with reasons](https://santimillang.github.io/ghostgpu/design/2026-07-31-behavioral-simulation-research/) |

**The metrics are attributed, and the attribution is correct.** `namespace`, `pod`, and `container` come straight from `ResourceClaim.status`, which the scheduler wrote — there is nothing to re-derive from a container runtime, which is where real exporters accumulate bugs.

What ghostgpu does *not* simulate is written down just as plainly — see the [fidelity contract](https://santimillang.github.io/ghostgpu/reference/fidelity/).

## What makes it different

**MIG-instance fidelity.** Overlapping profiles on one physical card are mutually exclusive, enforced by the upstream scheduler through DRA shared counters — ghostgpu contributes no allocation logic of its own.

**Fault injection.** Hardware failure is the hardest thing to test for, because you cannot arrange it on demand. Declare it instead, and the workload is evicted with its `ResourceClaim` released so it can reschedule.

**Attribution read from scheduler state.** `namespace`, `pod`, and `container` come from `ResourceClaim.status`, which the scheduler wrote — not re-derived from a container runtime, which is where exporters accumulate bugs under MIG.

## Development

Requires Linux (or WSL2 on Windows): `kubebuilder` ships no Windows binary, and `envtest` needs a Linux `kube-apiserver`.

```sh
make build        # build the manager
make build-cli    # build the ghostgpu CLI
make test         # unit tests + envtest
make test-e2e     # e2e against kwok + kind
```

Contributions are welcome — see [CONTRIBUTING.md](.github/CONTRIBUTING.md).

- [Code of Conduct](.github/CODE_OF_CONDUCT.md) · [Security policy and threat model](.github/SECURITY.md)
- [Changelog](CHANGELOG.md) · [Design notes and spike findings](docs/design)

## License

Apache-2.0 — see [LICENSE](LICENSE). Contributions require [DCO](https://developercertificate.org/) sign-off (`git commit -s`).
