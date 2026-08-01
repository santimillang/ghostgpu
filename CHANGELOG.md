# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While ghostgpu is pre-1.0, breaking changes may occur in minor releases. The `ghostgpu.dev/v1alpha1` API carries no compatibility guarantee.

The reasoning behind these decisions, including the spikes that reversed several of them, lives in [`docs/design/`](docs/design).

## [Unreleased]

## [0.1.0] - 2026-08-01

First release. Everything below is new.

### Added

**Simulated hardware**

- `GPUModel` and `GPUPool` cluster-scoped CRDs describing simulated GPU hardware and how it is advertised.
- `GPUPool` controller publishing a DRA `ResourceSlice` per matched kwok node, patching `nvidia.com/gpu` capacity and allocatable, and applying GPU Feature Discovery labels. Slices belonging to departed nodes are pruned.
- GFD-compatible node labels (`nvidia.com/gpu.present`, `.count`, `.product`, `.memory`, `.compute.major`, `.compute.minor`), matching NVIDIA's key names and value formats so existing tooling selects on simulated nodes unchanged.
- **MIG partitioning** via `spec.sharingMode: mig`: each GPU is divided into instances published as DRA partitionable devices, so overlapping profiles on one card are mutually exclusive. Exclusivity is enforced by the upstream scheduler through shared counters; ghostgpu contributes no allocation logic. Profiles come from built-in A100/H100 tables or `spec.migProfiles`.
- `spec.migPartition` declares which instances actually exist, modelling *static* MIG. Without it, dynamic MIG is modelled instead, as NVIDIA's DRA driver does.
- **Pre-existing occupancy** via `spec.occupancy`, so fragmentation scenarios exist before any workload is submitted. Occupied devices are still published but tainted, so the scheduler will not allocate them; lifting the occupancy releases them mid-test.
- **Fault injection** via `spec.faults`. `Evict` models device loss: the workload is thrown off and its `ResourceClaim` released, so it can reschedule onto healthy hardware. `Unschedulable` models a card that still runs but must take no new work. `xid` surfaces on `DCGM_FI_DEV_XID_ERRORS` and on the device taint.

**Metrics**

- **DCGM-shaped telemetry** on port 9400, dcgm-exporter's conventional port, so an existing scrape config or `ServiceMonitor` finds it unchanged. Metric and label names come from dcgm-exporter's default counter set and are pinned by a test.
- Readings are **attributed** to the pod holding each GPU — `namespace`, `pod`, `container`, and under MIG `GPU_I_ID` and `GPU_I_PROFILE` — read from `ResourceClaim.status` rather than re-derived from a container runtime. An idle device carries no workload labels at all, because an empty `pod` label is a distinct series that `sum by (pod)` would group on.
- Readings are **declared, not randomised** (`spec.utilization`), because a metric that jitters cannot be asserted against. Power and temperature are emitted only when declared — ghostgpu has no thermal or power model.
- `spec.utilization.workloads` gives different jobs different readings, which is the fixture idle-GPU reclamation and utilisation-based preemption actually need.

**Tooling**

- `ghostgpu up` turns a handful of flags into the `GPUModel`/`GPUPool` pair; `--dry-run` renders manifests without contacting a cluster.
- `ghostgpu capture` reads a cluster that already has GPUs and prints the manifests reproducing it. Strictly read-only, enforced by the type it holds rather than by its call sites.
- `ghostgpu status` reports which devices are published and who holds each one, with `--node` and `--budgets`.
- `ghostgpu version` reports what a binary was built from.

**Distribution**

- Multi-arch operator image, a single-file `install.yaml`, prebuilt CLI binaries for five platforms, and a Helm chart published as an OCI artifact whose values accept fleet definitions verbatim.
- Release images are signed with keyless cosign, carry SLSA build provenance, and ship SBOMs. CI verifies the signature it just produced.

**Project**

- End-to-end suite asserting real `kube-scheduler` behaviour against simulated hardware, plus four worked scenarios in [`examples/`](examples) that CI applies and checks.
- Apache-2.0, DCO sign-off, contributing guide, code of conduct, and a security policy with a threat model.

### Known limitations

- Under MIG **without a declared `spec.migPartition`**, the legacy extended-resource projection can be overcommitted: scalar resources cannot express that two profiles are the same silicon. The DRA path is faithful either way.
- Prometheus computes long windows such as `avg_over_time(...[24h])` from its own history, which ghostgpu cannot backfill. Testing such a rule means shortening its window. This is also why the behavioural phase timeline was deferred rather than built — see the [research note](https://github.com/santimillang/ghostgpu/blob/main/docs/design/2026-07-31-behavioral-simulation-research.md).
- ghostgpu is a testing tool for disposable clusters. It is not intended for production, and nobody has yet run it in anger.

[Unreleased]: https://github.com/santimillang/ghostgpu/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/santimillang/ghostgpu/releases/tag/v0.1.0
