# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While ghostgpu is pre-1.0, breaking changes may occur in minor releases. The `ghostgpu.dev/v1alpha1` API carries no compatibility guarantee.

## [Unreleased]

### Added

- `GPUModel` and `GPUPool` cluster-scoped CRDs describing simulated GPU hardware and how it is advertised (DRA `ResourceSlice`s and/or legacy `nvidia.com/gpu` extended resources).
- `GPUPool` controller. For every kwok-managed node matching a pool's selector it publishes a DRA `ResourceSlice`, patches `nvidia.com/gpu` capacity and allocatable, applies GPU Feature Discovery labels, and reports matched nodes and published devices on `status`. Slices belonging to departed nodes are pruned.
- GPU Feature Discovery-compatible node labels (`nvidia.com/gpu.present`, `.count`, `.product`, `.memory`, `.compute.major`, `.compute.minor`), matching NVIDIA GFD key names and value formats so existing tooling selects on simulated nodes unchanged.
- Project scaffolding: kubebuilder v4.15.0, Apache-2.0 license, CI for build, lint, unit/envtest, and e2e.
- Community health documentation: contributing guide, code of conduct, security policy with threat model, and governance.

### Changed

- API group corrected from `ghostgpu.ghostgpu.dev` to `ghostgpu.dev`. kubebuilder composes the group as `<group>.<domain>`, which doubled the name. Safe to change while unreleased; it would be breaking afterward.
- `spec.advertise.dra` and `spec.advertise.extendedResource` are now `*bool` rather than `bool`. A defaulted boolean with `omitempty` serializes an explicit `false` as absent, which the API server defaults straight back to `true`, making either path impossible to disable from a Go client.

### Fixed

- A `GPUPool` manifest that omitted `spec.advertise` entirely advertised nothing. The nested fields carried defaults but their parent object did not, so the API server had no object to default into and both paths resolved to `false`. `spec.advertise` now defaults to `{}`.

[Unreleased]: https://github.com/santimillang/ghostgpu/commits/main
