# MIG sharding spike — findings

**Date:** 2026-07-30
**Cluster:** kwok v0.8.0 on kind v0.32.0, Kubernetes v1.36.1, `resource.k8s.io/v1`

The v0.1 design named the counter-set sharding strategy "an explicit v0.2 design
item" and left it unanswered. Everything below is measured against a live
cluster, not read from documentation.

## Why sharding is unavoidable

MIG expansion multiplies device count: one physical GPU becomes one device per
profile. An 8-GPU node exposing 7 profiles is 56 devices, which fits in a single
slice. A 16-GPU node is 112, which does not.

## Q1 — Can a device consume a counter set declared in a *different* slice of the same pool? **YES**

This is the load-bearing question. If counters could not span slices, a physical
GPU could not span a shard boundary and the whole partitioning strategy would
have to change.

Layout: pool `node-a`, `resourceSliceCount: 3`.

| Slice | Contents |
|---|---|
| `shard-counters` | `sharedCounters` for `gpu-0` (80Gi memory, 7 slices) |
| `shard-devices-a` | `gpu-0-7g-80gb`, consuming the full budget |
| `shard-devices-b` | `gpu-0-1g-10gb`, consuming 1 slice / 10Gi |

| Step | Result |
|---|---|
| `mig-small` (slice B) scheduled first | **Running**, allocated `gpu-0-1g-10gb` |
| `mig-big` (slice A) requests the full budget | **Pending** — correctly blocked |
| *Negative control:* delete `mig-small` | `mig-big` → **Running** on `gpu-0-7g-80gb` |

The negative control confirms the shared counter set — not some unrelated
constraint — was the blocker. **Counter sets resolve pool-wide, not per-slice.**

## Q2 — Per-slice device limit with counters: **64, hard**

| Devices | Result |
|---|---|
| 64 | accepted |
| 65 | `spec.devices: Too many: 65: must have at most 64 items` |
| 128 | rejected |

The 128 figure from the v0.1 spike applies only to devices that do **not**
consume counters. Every MIG device consumes counters, so 64 is the number that
governs MIG sharding.

## Q3 — Per-slice `sharedCounters` limit: **8** (undocumented in the v0.1 spec)

| Counter sets | Result |
|---|---|
| 8 | accepted |
| 32, 33, 64, 65, 128 | `spec.sharedCounters: Too many: N: must have at most 8 items` |

**This is the tightest constraint in the whole design and the v0.1 spec did not
record it.** MIG needs one counter set per physical GPU, so a counter slice
holds at most 8 GPUs. Node sizes above 8 GPUs need multiple *counter* slices,
not merely multiple device slices — a case that 8-GPU nodes, the most common
configuration, would never surface.

## Q4 — Multiple counter slices in one pool: **YES**, and the scheduler resolves across them

Schema acceptance alone would not have settled this: server dry-run validates
shape, not scheduler behaviour. Verified live.

Layout: pool `node-ml`, `resourceSliceCount: 3` — counter slice 0 holds
`gpu-0`…`gpu-7`, counter slice 1 holds `gpu-8`…`gpu-9`, and a device slice holds
two overlapping profiles for **`gpu-9`**, whose counter set lives in the
*second* counter slice.

| Step | Result |
|---|---|
| `ml-a` requests `gpu-9-7g` (all 7 slices) | **Running** |
| `ml-b` requests `gpu-9-1g` on the same GPU | **Pending** — correctly blocked |
| *Negative control:* delete `ml-a` | `ml-b` → **Running** on `gpu-9-1g` |

## Resulting sharding strategy

```
counterSlices      = ceil(gpusPerNode / 8)
deviceSlices       = ceil(gpusPerNode * len(migProfiles) / 64)
resourceSliceCount = counterSlices + deviceSlices
```

| Node | Counter slices | Device slices | Total |
|---|---|---|---|
| 8 GPUs × 7 profiles | 1 | 1 | 2 |
| 16 GPUs × 7 profiles | 2 | 2 | 4 |
| 128 GPUs × 7 profiles (CRD maximum) | 16 | 14 | 30 |

Because counter sets resolve pool-wide, shard assignment is otherwise free: a
GPU's profiles may be split across device slices, and profiles may be packed
without regard to which counter slice holds their GPU. Assignment should still
be deterministic so that a restarted operator republishes identical slices
rather than churning them.

## Open design decision, not a research question

Under MIG, what does the legacy extended resource advertise? Real clusters use
NVIDIA's `mixed` strategy (`nvidia.com/mig-1g.10gb: N`) or `single`
(`nvidia.com/gpu` counting MIG instances). This is a product choice about which
real-world behaviour to mirror; both are expressible.

## Reproduction

Scratch manifests: `scratchpad/{shard-spike,shard-pods,multi-live}.yaml`
(throwaway, not part of the repo). Cluster teardown:
`kwokctl delete cluster --name ghostgpu-spike`.
