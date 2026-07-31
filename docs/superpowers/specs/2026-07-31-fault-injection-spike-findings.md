# Fault injection spike findings

**Date:** 2026-07-31
**Status:** mechanism settled; design below is ready to plan against

Tested against a live kwok cluster on Kubernetes v1.36 with negative controls,
on a cluster created with only the gates the ghostgpu quickstart uses
(`DynamicResourceAllocation=true`, `resource.k8s.io/v1=true`).

## The question

Occupancy uses a `NoSchedule` device taint, which only keeps *new* claims away.
A hardware fault is a different thing: the GPU dies while a job is holding it,
and what happens to that job is the whole point of testing remediation. So:

**can a simulated fault reclaim a GPU from a workload already running on it?**

## Finding — `NoExecute` device taints evict, and deallocate the claim

| Step | Result |
| --- | --- |
| Control: pod running, claim allocated to `gpu-0` | `Running`, `gpu-0` |
| Add a `NoExecute` taint to `gpu-0` in the ResourceSlice | pod **deleted**, claim's allocation **cleared** |
| Negative control: identical pod, no taint, same 25s wait | still `Running`, still holds `gpu-0` |

The negative control is what makes this a finding rather than a coincidence.
A pod vanishing proves nothing on its own — kwok drives pod lifecycle, and
something else could have removed it. An identical pod surviving the same wait
is what pins the cause to the taint.

The claim being deallocated matters as much as the eviction: it means the
workload can be rescheduled onto healthy hardware, which is the behaviour any
remediation or requeueing system under test actually needs to exercise.

No extra feature gates were needed, matching the occupancy spike's finding for
`NoSchedule`.

### A caveat, stated rather than glossed

A follow-up check showed a two-GPU claim staying `Pending` once one card was
tainted, which is consistent with the faulted card leaving the allocatable pool.
That step did **not** get its own negative control, so it is reported here as
consistent-with rather than proven. The occupancy spike proved the equivalent
property for `NoSchedule` with a full control, and there is no reason to think
`NoExecute` is weaker, but the distinction is worth keeping honest.

### A mistake worth recording

The first run of this spike used `device.name == "gpu-0"` in a CEL selector.
There is no `name` field on a DRA device, so the ResourceClaim was rejected, the
pod stayed `Pending` for that reason, and the run "showed" no eviction. It
proved nothing.

The lesson is the one this project keeps relearning: a negative result is only
information if the control passed first. The rerun asserts the control and
aborts if it fails, rather than printing a verdict either way.

Device attributes are addressed as `device.attributes["<driver>"].<name>`.

## Design implied by the findings

Two effects, mapping onto how real GPU failures actually present:

```yaml
spec:
  faults:
    - nodeSelector: {rack: a}
      gpus: 1              # lowest-index-first, like occupancy
      xid: 79              # surfaced on DCGM_FI_DEV_XID_ERRORS
      effect: Evict        # NoExecute: the card is gone, the job must move
    - nodeSelector: {rack: b}
      xid: 63
      effect: Unschedulable  # NoSchedule: degraded, drain before reboot
```

- **`Evict`** models device loss and uncorrectable ECC — XID 48, 79. The
  workload is thrown off and its claim released.
- **`Unschedulable`** models a card that still runs but must not take new work,
  such as a pending row remap. This is the same primitive occupancy already uses.
- **`xid`** sets `DCGM_FI_DEV_XID_ERRORS` for that GPU, which is the signal most
  remediation tooling actually watches. The metrics work landed the series
  already, so this is a value change rather than new surface.

Everything composes primitives that already exist and are already tested:
device taints from occupancy, the metrics pipeline from v0.3. That is the
argument for doing fault injection before behavioural workload simulation,
which needs new time-varying machinery instead.

Open points for the plan:

- **Faults are a property of a scenario, not of hardware** — the same tension
  occupancy had, and resolved by living on `GPUPool` because that is what the
  controller already reconciles. Same answer here unless something argues
  otherwise.
- **`ghostgpu status` should show faulted distinctly** from occupied and
  allocated. Three states now, and conflating any two would misreport who holds
  what and why.
- **Interaction with occupancy** on the same GPU needs a defined precedence:
  a faulted card is not merely busy, and should report as faulted.
