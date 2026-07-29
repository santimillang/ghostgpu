# ghostgpu

Simulate GPU clusters on Kubernetes. Test GPU-aware schedulers, autoscalers, and platform tooling with **zero GPU hardware**.

ghostgpu builds on [kwok](https://kwok.sigs.k8s.io/) and publishes Dynamic Resource Allocation (DRA) `ResourceSlice`s plus legacy extended-resource capacity, so a real `kube-scheduler` makes real placement decisions against hardware that does not exist.

> **Status:** early development. Not yet usable.

## Why

Testing GPU scheduling logic normally requires GPUs — expensive to idle, slow to provision, and impractical in CI. ghostgpu lets you run the real thing (Kueue, Volcano, KEDA, your own operator) against simulated fleets on a laptop.

Already verified against a real `kube-scheduler`: pods are placed against simulated GPU capacity and correctly refused once that capacity is exhausted — including MIG-style partitioning, where overlapping profiles on the same physical GPU are mutually exclusive.

## Planned capabilities

| Area | Status |
|---|---|
| DRA `ResourceSlice` publication + legacy `nvidia.com/gpu` capacity | in progress (v0.1) |
| MIG / partitionable devices | planned (v0.2) |
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
make test                 # unit tests + envtest
make test-e2e             # e2e against kwok + kind
```

## License

Apache-2.0. Contributions require [DCO](https://developercertificate.org/) sign-off (`git commit -s`).
