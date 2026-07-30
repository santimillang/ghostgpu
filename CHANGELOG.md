# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While ghostgpu is pre-1.0, breaking changes may occur in minor releases. The `ghostgpu.dev/v1alpha1` API carries no compatibility guarantee.

## [Unreleased]

### Added

- **MIG partitioning.** `GPUPool.spec.sharingMode: mig` divides each simulated GPU into MIG instances published as DRA partitionable devices, so overlapping profiles on one physical card are mutually exclusive — enforced by the upstream scheduler through shared counters, with no allocation logic in ghostgpu. Profiles come from built-in tables for the A100 (40GB and 80GB) and H100, or from `spec.migProfiles` for hardware ghostgpu does not recognise.
- Sharded ResourceSlice publication. Two measured API limits force the layout — at most 8 `sharedCounters` and 64 counter-consuming devices per slice — so a node becomes `ceil(gpus/8)` counter slices plus `ceil(gpus × profiles/64)` device slices in one DRA pool. Counter sets resolve pool-wide, so a GPU's profiles may straddle a slice boundary.
- Mixed-strategy node advertisement under MIG: one extended resource per profile (`nvidia.com/mig-1g.10gb`), `nvidia.com/gpu` at zero, and the `nvidia.com/mig.capable` / `nvidia.com/mig.strategy` labels. Per-profile counts are derived from both the compute-slice and memory budgets, matching NVIDIA's published instance counts.
- `ghostgpu up --sharing-mode mig [--mig-profiles 1g.10gb,3g.40gb]`.
- `GPUModel` and `GPUPool` cluster-scoped CRDs describing simulated GPU hardware and how it is advertised (DRA `ResourceSlice`s and/or legacy `nvidia.com/gpu` extended resources).
- `GPUPool` controller. For every kwok-managed node matching a pool's selector it publishes a DRA `ResourceSlice`, patches `nvidia.com/gpu` capacity and allocatable, applies GPU Feature Discovery labels, and reports matched nodes and published devices on `status`. Slices belonging to departed nodes are pruned.
- GPU Feature Discovery-compatible node labels (`nvidia.com/gpu.present`, `.count`, `.product`, `.memory`, `.compute.major`, `.compute.minor`), matching NVIDIA GFD key names and value formats so existing tooling selects on simulated nodes unchanged.
- `ghostgpu up` CLI, turning a handful of flags into the `GPUModel`/`GPUPool` pair so that adoption does not require hand-writing custom resources. Re-running applies changes rather than failing on AlreadyExists, `--dry-run` renders manifests without contacting a cluster, and the command reports how many devices the operator published once it has observed the new spec.
- End-to-end suite asserting real kube-scheduler behaviour against simulated hardware: extended-resource placement and refusal to overcommit, DRA allocation honouring an NVLink topology selector, GFD labels, published `ResourceSlice`s, pool status, and the safety invariant. The e2e cluster is now kwok on kind with `DynamicResourceAllocation` enabled, because a plain kind cluster can host the manager but cannot exercise anything it does.
- Project scaffolding: kubebuilder v4.15.0, Apache-2.0 license, CI for build, lint, unit/envtest, and e2e.
- Community health documentation: contributing guide, code of conduct, security policy with threat model, and governance.

### Changed

- API group corrected from `ghostgpu.ghostgpu.dev` to `ghostgpu.dev`. kubebuilder composes the group as `<group>.<domain>`, which doubled the name. Safe to change while unreleased; it would be breaking afterward.
- `spec.advertise.dra` and `spec.advertise.extendedResource` are now `*bool` rather than `bool`. A defaulted boolean with `omitempty` serializes an explicit `false` as absent, which the API server defaults straight back to `true`, making either path impossible to disable from a Go client.

### Known limitations

- Under MIG, the legacy extended-resource path can be overcommitted. Scalar resources cannot express that profiles on one GPU are mutually exclusive, so a node advertising both `nvidia.com/mig-1g.10gb` and `nvidia.com/mig-7g.80gb` will let a scheduler allocate from both at once. Each count is faithful alone; their sum is not. The DRA path models the exclusion correctly. Tracked as a fidelity item in the design spec, with `spec.migPartition` planned to close it by declaring which instances actually exist.

### Fixed

- A `GPUPool` manifest that omitted `spec.advertise` entirely advertised nothing. The nested fields carried defaults but their parent object did not, so the API server had no object to default into and both paths resolved to `false`. `spec.advertise` now defaults to `{}`.

[Unreleased]: https://github.com/santimillang/ghostgpu/commits/main
