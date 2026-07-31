# Occupancy spike findings

**Date:** 2026-07-31
**Issue:** [#25](https://github.com/santimillang/ghostgpu/issues/25) — express pre-existing occupancy so fragmentation scenarios are testable
**Status:** settled; design below is ready to plan against

Every mechanism below was tested against a live kwok cluster on Kubernetes
v1.36, with negative controls. Nothing here is read from documentation.

## The question

ghostgpu can only produce empty fleets. A large share of real GPU scheduling
bugs are about fragmentation rather than capacity — "7 GPUs free but spread
2/2/2/1 across four nodes; does my 4-GPU job schedule?" — and that situation
cannot be constructed deterministically today.

The issue flagged the hard part correctly: under DRA, allocation lives in
`ResourceClaim.status`, which the scheduler owns. Writing it directly would be
forging state the scheduler believes it owns. So the spike asked **what can
represent "busy" honestly**, not merely what would produce the right numbers.

## Finding 1 — DRA device taints are the mechanism, and they need no feature gate

Devices in a `resource.k8s.io/v1` `ResourceSlice` carry an inline `taints`
field. ghostgpu already owns and republishes those slices, so occupancy needs
**no new object, no new controller, and no state the scheduler owns**.

Tested on a cluster created with only the gates the ghostgpu quickstart uses
(`DynamicResourceAllocation=true`, `resource.k8s.io/v1=true`) — deliberately
*without* `DRADeviceTaints`:

| Step | Result |
| --- | --- |
| Control: two devices, one tainted, claim may take either | allocated `gpu-free`, pod Running |
| Test: second claim, only the tainted device remains | no allocation, pod **Pending** |
| Negative control: remove the taint from the slice | the *same pending claim* allocated `gpu-busy`, pod Running |

The third row is the load-bearing one. It proves the Pending was caused by the
taint and nothing else, and that lifting occupancy releases the device without
recreating anything — so a scenario can free a GPU mid-test.

An earlier run reached the same result *with* `DRADeviceTaints=true`, so the
gate is not what makes this work: device taints are on by default in 1.36.

### Do not use `DeviceTaintRule`

The cluster serves `resource.k8s.io/v1alpha3 DeviceTaintRule`, and the API
server warns:

> DeviceTaintRule is deprecated in v1.36+, unavailable in v1.39+

Inline `spec.devices[].taints` on a `v1` ResourceSlice is the durable API and
the one ghostgpu should use. Building on the alpha object would buy a rewrite.

## Finding 2 — the legacy path expresses occupancy as `allocatable < capacity`

Scalar extended resources have no taint concept, but they do not need one.
Setting `nvidia.com/gpu` capacity 8 with allocatable 2:

| Pod | Result |
| --- | --- |
| requests 2 (all of allocatable) | Running |
| requests 1 more (capacity says 8) | **Pending** |

The scheduler honours allocatable and ignores the larger capacity. This is
semantically exactly right rather than a trick: allocatable already means "of
this node's capacity, this much is available to this scheduler", which is
precisely what `--system-reserved` expresses for CPU and memory. Occupancy on
the legacy path is therefore *not* an approximation — unlike the MIG
projection, it is faithful.

## Finding 3 — forging allocation works, so refusing it must be a choice

`kubectl patch resourceclaim --subresource=status` with a fabricated
`status.allocation` was **accepted and read back intact**. Nothing in the API
server prevents ghostgpu from forging scheduler state.

That matters: the guard cannot be "it is impossible", it has to be a deliberate
design commitment. Recorded here so the option is rejected on the record rather
than assumed away. Given findings 1 and 2 both work, there is no reason to
revisit it.

## Design implied by the findings

Occupancy is a property of a *test case*, not of hardware, but both mechanisms
above act on objects the `GPUPool` controller already reconciles — the
ResourceSlice it publishes and the node capacity it patches. Splitting it into a
separate scenario CRD would mean a second controller writing the same two
objects, with all the conflict that implies.

So: a field on `GPUPool`, reconciled by the existing controller.

```yaml
spec:
  gpusPerNode: 8
  occupancy:
    - nodeSelector: {rack: a}
      busyPerNode: 6      # 2 free per node in rack a
    - nodeSelector: {rack: b}
      busyPerNode: 0
```

Reconciled as: taint the first `busyPerNode` devices of each matching node's
slice, and set that node's `nvidia.com/gpu` allocatable to
`gpusPerNode - busyPerNode` while leaving capacity at `gpusPerNode`. Both are
pure functions of the spec, so the state is deterministic and survives an
operator restart by construction — which is the acceptance criterion.

Entries are matched **first-match-wins**, so a list can express a fleet that is
unevenly full — which is the entire point. An entry with no selector matches
every node and works as a default when placed last.

### Decisions taken

- **`busyPerNode` counts physical GPUs in both sharing modes.** Under MIG that
  means every instance carved from an occupied card is tainted. This is not a
  simplification but the only correct reading: MIG instances draw from shared
  counters, so tainting a single `1g.10gb` would leave a `7g.80gb` on the same
  silicon allocatable, and the card would not be busy at all. One meaning of
  the field across both modes, and MIG needs no separate design.
- **Occupancy is lowest-index-first**, so a scenario is reproducible rather
  than depending on map order.
- **Partial-instance occupancy is deferred.** "This card has 3 of its 7 slices
  used" is a genuinely different feature: it needs the counters drawn down
  rather than the devices removed, and DRA offers no way to reserve part of a
  counter set without an allocation. Out of scope here, and worth its own issue
  if anyone asks for it.
- **`ghostgpu status` must show occupied distinctly from allocated.** They are
  different states — one is ghostgpu's doing, the other the scheduler's — and
  conflating them would make the budget view lie about who holds what.

## Rejected

| Option | Why |
| --- | --- |
| Write `ResourceClaim.status.allocation` | Forges state the scheduler owns; works (finding 3), which makes rejecting it a commitment rather than a constraint |
| `DeviceTaintRule` objects | v1alpha3, unavailable in v1.39+ |
| Operator-created filler pods and claims | Real allocation, but placement depends on the scheduler, so the scenario under test is not deterministic — the exact problem the issue raises |
| Documented filler-pod recipe only | The issue suggested trying this first. Finding 1 makes it unnecessary: the honest mechanism costs one spec field and no new object, and a recipe cannot deliver determinism at all |
