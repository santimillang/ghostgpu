# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While ghostgpu is pre-1.0, breaking changes may occur in minor releases. The `ghostgpu.dev/v1alpha1` API carries no compatibility guarantee.

## [Unreleased]

### Added

- `GPUModel` and `GPUPool` cluster-scoped CRDs describing simulated GPU hardware and how it is advertised (DRA `ResourceSlice`s and/or legacy `nvidia.com/gpu` extended resources).
- Project scaffolding: kubebuilder v4.15.0, Apache-2.0 license, CI for build, lint, unit/envtest, and e2e.
- Community health documentation: contributing guide, code of conduct, security policy with threat model, and governance.

### Changed

- API group corrected from `ghostgpu.ghostgpu.dev` to `ghostgpu.dev`. kubebuilder composes the group as `<group>.<domain>`, which doubled the name. Safe to change while unreleased; it would be breaking afterward.

[Unreleased]: https://github.com/santimillang/ghostgpu/commits/main
