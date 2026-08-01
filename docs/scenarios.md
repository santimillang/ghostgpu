# Scenarios

[`examples/`](https://github.com/santimillang/ghostgpu/tree/main/examples) holds worked scenarios, each answering a question you might need to ask of your own tooling.

**All of them are applied and checked by CI**, so a scenario that stops working fails the build.

## Fragmented fleet

*Seven GPUs are free, spread across four nodes. Does my four-GPU job schedule? Should it? Does my autoscaler add a node?*

A fleet that starts unevenly full, so the fragmentation exists before any workload is submitted. The four-GPU job stays `Pending` while seven GPUs sit free — and the interesting question is whether your tooling does the right thing about it.

→ [`examples/fragmented-fleet`](https://github.com/santimillang/ghostgpu/tree/main/examples/fragmented-fleet) · [how occupancy works](simulating/occupancy.md)

## GPU failure under a running job

*A card fails while a job is using it. Does my remediation drain the node? Does the job come back?*

The GPU is declared failed with an XID; the workload is evicted and its `ResourceClaim` released, so it can reschedule onto healthy hardware. Removing the fault returns the hardware to service.

→ [`examples/gpu-failure`](https://github.com/santimillang/ghostgpu/tree/main/examples/gpu-failure) · [how faults work](simulating/faults.md)

## MIG exclusivity

*Two profiles overlap on one physical card. Does the scheduler refuse the second?*

Asserted with a negative control, so the refusal is demonstrated rather than assumed.

→ [`examples/mig-exclusivity`](https://github.com/santimillang/ghostgpu/tree/main/examples/mig-exclusivity) · [how MIG works](simulating/mig.md)

## Idle reclamation

*One notebook is squatting on a GPU at 4% while a trainer beside it runs at 90%. Does my reclamation tool pick the right one?*

Per-workload utilisation makes the fleet heterogeneous, which is the whole point: a fleet where every allocated card reports the same number cannot ask this question.

→ [`examples/idle-reclamation`](https://github.com/santimillang/ghostgpu/tree/main/examples/idle-reclamation) · [how metrics work](simulating/metrics.md)

!!! note "Long Prometheus windows"
    Real reclamation rules often use windows like `avg_over_time(...[24h])`, computed by Prometheus from its own history, which ghostgpu cannot backfill. Testing such a rule means shortening its window.
