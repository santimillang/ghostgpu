# Fidelity contract

ghostgpu is only as valuable as its fidelity claims are honest. A simulator that quietly overstates what it reproduces is worse than no simulator, because you would build tests on it.

Every simulated behaviour falls into one of three classes.

## Faithful

Byte-comparable to the real thing. If your tooling parses it, it cannot tell the difference.

- **Object shapes** — `ResourceSlice`, `ResourceClaim`, node `capacity`/`allocatable`
- **DCGM metric and label names** — taken from dcgm-exporter's default counter set, pinned by a test
- **GFD node labels** — key names and value formats matching NVIDIA's GPU Feature Discovery
- **MIG instance counts per profile** — matching NVIDIA's published tables
- **Scheduler behaviour** — because there is none of ours; the upstream `kube-scheduler` makes every placement decision, and DRA shared counters enforce MIG exclusivity

## Approximated

Plausible, but not measured from hardware. Useful for driving a rule under test, not for predicting what real hardware would do.

- **Utilisation values** — declared, not modelled. They are whatever you say they are, deliberately, because a metric that jitters cannot be asserted against.
- **Framebuffer used and free** — derived from declared percentages of the GPU's own memory

## Not simulated

Explicitly out of scope. ghostgpu does not attempt these, and will report rather than fake them.

- **CUDA execution** — nothing runs on the simulated device
- **Interconnect bandwidth** — NVLink domains are modelled as *topology*, so a scheduler can express affinity, but no transfer rate exists
- **Driver internals** — no `nvidia-smi`, no device files, no `podresources` on the kwok path
- **Power and temperature** — emitted only when declared. ghostgpu has no thermal or power model, and a plausible-looking wattage would be fabrication.
- **Time-series history** — Prometheus computes long windows from its own TSDB, which ghostgpu cannot backfill
- **Partial-instance MIG occupancy** — a card is occupied or it is not

## The rule for contributors

Do not silently upgrade something from *approximated* to *faithful* without evidence. Where a claim is about scheduler or API-server behaviour, the evidence has to come from a live cluster: envtest runs no `kube-scheduler`, so it cannot settle a scheduling question.

Several design decisions in this project were reversed by spikes that contradicted the documentation — those are recorded in the [design notes](../design/2026-07-29-spike-findings.md).
