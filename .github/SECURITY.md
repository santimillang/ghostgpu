# Security Policy

## Reporting a vulnerability

**Do not open a public issue for security vulnerabilities.** Report privately through [GitHub's private vulnerability reporting](https://github.com/santimillang/ghostgpu/security/advisories/new), including what the vulnerability allows, how to reproduce it, and the affected versions.

ghostgpu is pre-1.0 and maintained by one person, so please allow reasonable time before public disclosure. Only the latest release receives fixes.

## Threat model

Deliberately specific, because ghostgpu holds cluster-wide permissions that would be dangerous if misused.

### What ghostgpu is

A **testing tool**, designed for disposable development and CI clusters. It is explicitly **not** intended for production clusters, and running it in one is outside its supported use.

### Privileges it holds

- `patch`/`update` on `nodes` and `nodes/status` — to advertise simulated GPU capacity and labels
- `create`/`update`/`delete` on `resourceslices` — to publish DRA devices
- read/write on its own `ghostgpu.dev` CRDs

`nodes/status` write access is the sensitive one: an attacker who compromised the operator could advertise false capacity on real nodes, causing the scheduler to place workloads the node cannot actually run.

### Primary control: the simulated-node invariant

ghostgpu must never modify a Node lacking the `kwok.x-k8s.io/node` annotation. This is enforced in code (`safety.IsSimulatedNode`), called on every write path, and covered by a test asserting real nodes are left untouched.

This is defence against *accident* — an operator pointed at the wrong cluster, or a `nodeSelector` matching more broadly than intended. It is not, on its own, defence against a compromised operator, which would hold the RBAC to bypass it.

### The simulated-GPU metrics endpoint

The DCGM-shaped metrics on port 9400 are served by a plain handler behind a ClusterIP service, with **no authentication or authorization**. Anything that can reach the service can read them, and they include the namespace, pod, and container names of the workloads holding simulated GPUs.

This matches real dcgm-exporter, which is also unauthenticated, and it is a deliberate choice: an existing scrape config should find this endpoint unchanged. It is called out because it is a different posture from the operator's *own* metrics on port 8443, which are protected by authentication and authorization filters.

If the workload names in a cluster are themselves sensitive, restrict access with a NetworkPolicy.

### Deployment guidance

- Do not install ghostgpu in a production cluster.
- Scope its RBAC to the minimum your scenario needs. Disabling `advertise.extendedResource` means ghostgpu stops writing `nodes/status`, which lets you scope that permission down — the shipped ClusterRole is static and still grants it, so this is a change you make, not one the setting makes for you. The GFD node labels still require `nodes` write regardless.
- Treat any cluster running ghostgpu as untrusted for scheduling-correctness purposes: by design, the capacity it reports is fiction.

### Out of scope

- Denial of service from deliberately configuring implausibly large simulated fleets — that is a supported use (scale testing), and resource limits are the operator's responsibility.
- The simulated GPUs reporting false telemetry. That is the entire point of the project.
