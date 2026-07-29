# Security Policy

## Reporting a vulnerability

**Do not open a public issue for security vulnerabilities.**

Report privately through [GitHub's private vulnerability reporting](https://github.com/santimillang/ghostgpu/security/advisories/new). This creates a private advisory visible only to maintainers.

Please include:

- What the vulnerability allows an attacker to do
- Steps to reproduce, ideally with a minimal manifest or test case
- Affected versions
- Any suggested mitigation

You can expect an initial response within 7 days. ghostgpu is currently maintained by a single maintainer, so please allow reasonable time before public disclosure — we will keep you informed of progress and credit you in the advisory unless you prefer otherwise.

## Supported versions

ghostgpu is pre-1.0 and under active development. Only the latest release receives security fixes.

| Version | Supported |
|---|---|
| `main` | ✅ |
| pre-releases | ❌ |

## Threat model

This section is deliberately specific, because ghostgpu holds cluster-wide permissions that would be dangerous if misused.

### What ghostgpu is

A **testing tool**. It is designed to run in disposable development and CI clusters. It is explicitly **not** intended for production clusters, and running it in one is outside its supported use.

### Privileges it holds

The operator requires:

- `patch`/`update` on `nodes` and `nodes/status` — to advertise simulated GPU capacity and labels
- `create`/`update`/`delete` on `resourceslices` — to publish DRA devices
- read/write on its own `ghostgpu.dev` CRDs

`nodes/status` write access is the sensitive one: an attacker who compromised the operator could advertise false capacity on real nodes, causing the scheduler to place workloads that the node cannot actually run.

### Primary control: the simulated-node invariant

ghostgpu must never modify a Node that lacks the `kwok.x-k8s.io/node` annotation. This is enforced in code (`safety.IsSimulatedNode`), called on every write path, and covered by a test that asserts real nodes are left untouched.

This is defense against *accident* — an operator pointed at the wrong cluster, or a `nodeSelector` that matches more broadly than intended. It is not, on its own, defense against a compromised operator, which would hold the RBAC to bypass it.

### Deployment guidance

- Do not install ghostgpu in a production cluster.
- Scope its RBAC to the minimum your scenario needs; disable `advertise.extendedResource` if you only test DRA, which removes the need for `nodes/status` writes.
- Treat any cluster running ghostgpu as untrusted for scheduling-correctness purposes: by design, the capacity it reports is fiction.

### Out of scope

- Denial of service caused by deliberately configuring implausibly large simulated fleets — that is a supported use (scale testing), and resource limits are the operator's responsibility.
- The simulated GPUs reporting false telemetry. That is the entire point of the project.
