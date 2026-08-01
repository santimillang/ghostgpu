# Starting from a full cluster

Most interesting GPU scheduling bugs are about fragmentation rather than capacity:

> Seven GPUs are free, but spread 2/2/2/1 across four nodes — does my four-GPU job schedule? Should it? Does my autoscaler add a node, and is that the right call?

Declare how full the fleet starts and that scenario exists before any workload is submitted:

```yaml
spec:
  gpusPerNode: 4
  occupancy:
    - nodeSelector: {rack: c}
      busyPerNode: 3
    - busyPerNode: 2      # no selector: the default for everything else
```

Entries are first-match-wins, so ordering is meaningful and a selector-less entry reads as a default. `ghostgpu up --busy-per-node 2` covers the even case from one flag.

## Why not filler pods

Building this with filler pods is racy and, more importantly, **not reproducible**: where the filler lands is the scheduler's choice, so the scenario under test is never quite the one intended.

## How it works

Occupied GPUs are still published — a busy fleet has the same hardware as an idle one — but they carry a DRA device taint, so the upstream scheduler will not allocate them. The legacy path advertises `allocatable` below `capacity`.

Lifting the occupancy releases the devices, so a pending job can be made schedulable mid-test.

Both paths were verified against a live scheduler with negative controls before the design was chosen: a claim is refused when only occupied devices remain, and the *same* pending claim is placed the moment the occupancy is lifted. See the [occupancy spike findings](../design/2026-07-31-occupancy-spike-findings.md).

## What ghostgpu will not do

**ghostgpu never writes `ResourceClaim.status`.** A spike confirmed that it *could* — nothing in the API server prevents a fabricated allocation from being accepted and read back — which is what makes refusing a commitment rather than a constraint.

Allocation is state the scheduler owns. Forging it would make the simulation lie to the very component under test.
