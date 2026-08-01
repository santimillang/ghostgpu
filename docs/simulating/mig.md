# MIG

Partition each GPU into MIG instances and let the scheduler enforce that overlapping profiles on one card are mutually exclusive:

```sh
ghostgpu up --gpu NVIDIA-H100-80GB-HBM3 --sharing-mode mig --gpus-per-node 16
```

Profiles come from built-in tables matching NVIDIA's published instance counts, so an H100 offers seven `1g.10gb` per card but only four `1g.20gb` — memory binds before compute slices do. `--mig-profiles 1g.10gb,3g.40gb` restricts a pool to a subset.

Exclusivity is enforced by the upstream scheduler through DRA shared counters; **ghostgpu contributes no allocation logic**.

## Dynamic versus static MIG

By default this models **dynamic MIG**, as NVIDIA's DRA driver does: every profile is offered and the scheduler picks.

To model **static MIG**, where an administrator pre-created the instances, declare them:

```sh
ghostgpu up --gpu NVIDIA-H100-80GB-HBM3 --sharing-mode mig \
  --mig-partition 3g.40gb=1,1g.10gb=4
```

The distinction matters for the legacy extended-resource projection (`nvidia.com/mig-1g.10gb` and friends, NVIDIA's `mixed` strategy). Scalar resources cannot say "these two are the same silicon", so under dynamic MIG a node advertises alternatives whose *sum* no card could satisfy — each count is right, their total is not.

**A declared partition makes that projection exact**, because the declared instances all coexist. The DRA path is faithful either way.

A partition that cannot fit one GPU is rejected with `MIGPartitionInvalid`, and nothing is published.

## How slices are laid out

Two measured API limits force the layout — at most 8 `sharedCounters` and 64 counter-consuming devices per `ResourceSlice`. A node therefore becomes `ceil(gpus/8)` counter slices plus `ceil(gpus × profiles/64)` device slices in one DRA pool. Counter sets resolve pool-wide, so a GPU's profiles may straddle a slice boundary.

Both limits were measured against a live API server rather than read from documentation — see the [MIG sharding findings](../design/2026-07-30-mig-sharding-findings.md).

## Interaction with occupancy

Under `sharingMode: mig`, `busyPerNode` counts whole cards, and every instance carved from an occupied one becomes unavailable. That is the only correct reading: MIG instances draw on shared counters, so leaving one profile allocatable would mean the card was never occupied.

Partial-instance occupancy is deliberately out of scope.
