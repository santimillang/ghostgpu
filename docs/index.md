# ghostgpu

Simulate GPU clusters on Kubernetes. Test GPU-aware schedulers, autoscalers, and platform tooling with **zero GPU hardware**.

ghostgpu builds on [kwok](https://kwok.sigs.k8s.io/) and publishes Dynamic Resource Allocation (DRA) `ResourceSlice`s plus legacy extended-resource capacity, so a real `kube-scheduler` makes real placement decisions against hardware that does not exist.

!!! warning "Early development"
    The core works and is covered end-to-end against a real `kube-scheduler`, but the `v1alpha1` API carries no compatibility guarantee yet.

## Why

Testing GPU scheduling logic normally requires GPUs — expensive to idle, slow to provision, and impractical in CI. ghostgpu lets you run the real thing (Kueue, Volcano, KEDA, your own operator) against simulated fleets on a laptop, including [a copy of the fleet you actually run](tools/capture.md), read out of your own cluster.

Already verified against a real `kube-scheduler`: pods are placed against simulated GPU capacity and correctly refused once that capacity is exhausted — including MIG-style partitioning, where overlapping profiles on the same physical GPU are mutually exclusive.

## What it can simulate

| Area | Status |
|---|---|
| DRA `ResourceSlice` publication, `nvidia.com/gpu` capacity, GFD labels | working |
| [MIG / partitionable devices](simulating/mig.md) | working |
| [Pre-existing occupancy and fragmentation](simulating/occupancy.md) | working |
| [DCGM-shaped metrics with per-pod attribution](simulating/metrics.md) | working |
| [Fault injection](simulating/faults.md) — XID, device loss, drain-before-reboot | working |
| Behavioral phase timeline (download → warmup → training) | deferred, [see the research](https://github.com/santimillang/ghostgpu/blob/main/docs/design/2026-07-31-behavioral-simulation-research.md) |

## The differentiator

**The metrics are attributed, and the attribution is correct.**

`namespace`, `pod`, and `container` — and under MIG `GPU_I_ID` and `GPU_I_PROFILE` — come straight from `ResourceClaim.status`, which the scheduler wrote. That is the payoff of the DRA-first design: there is nothing to re-derive from a container runtime, which is where real exporters accumulate bugs.

## Prior art, and what makes this different

[`fake-gpu-operator`](https://github.com/run-ai/fake-gpu-operator) (run:ai) is actively maintained and already covers capacity advertising, dynamic GPU-utilization metrics, and basic DRA on kwok. **If that is all you need, use it** — it is the mature option.

ghostgpu's genuine deltas are three:

**MIG-instance exclusivity.** Overlapping profiles on one physical card are mutually exclusive, enforced by the upstream scheduler through DRA shared counters — ghostgpu contributes no allocation logic of its own.

**Declarative fault injection.** Hardware failure is the hardest thing to test for, because you cannot arrange it on demand. Declare it instead, and the workload is evicted with its `ResourceClaim` released so it can reschedule onto healthy hardware.

**Attribution read from scheduler state.** `namespace`, `pod`, and `container` come from `ResourceClaim.status`, which the scheduler wrote — not re-derived from a container runtime, which is where exporters accumulate labelling bugs under MIG.

## Safety

ghostgpu only ever modifies nodes carrying kwok's `kwok.x-k8s.io/node` annotation. A node without it is never touched, whatever a pool selector matches — see the [threat model](https://github.com/santimillang/ghostgpu/blob/main/.github/SECURITY.md).
