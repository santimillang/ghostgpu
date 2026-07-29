# Spike Findings — kwok + GPU simulation feasibility

**Date:** 2026-07-29
**Purpose:** Resolve blockers B1 (pod→GPU binding without a kubelet) and B2 (DRA viability without a kubelet) before committing to an implementation plan.
**Environment:** kwokctl v0.8.0, kind v0.32.0, kind node image v1.36.1, Docker Desktop, Windows 11. Cluster created with `--kube-feature-gates DynamicResourceAllocation=true --kube-runtime-config resource.k8s.io/v1=true`.

All results below were observed directly, not inferred.

## Q1 — Can patched fake-node capacity drive real scheduler decisions? **YES**

Applied a kwok fake Node, then patched its `status` subresource:

```
kubectl patch node ghost-node-0 --subresource=status --type=merge \
  -p '{"status":{"capacity":{"nvidia.com/gpu":"8"},"allocatable":{"nvidia.com/gpu":"8"}}}'
```

| Pod | Request | Result |
|---|---|---|
| `gpu-job-a` | 4 GPUs | **Running** on `ghost-node-0` |
| `gpu-job-b` | 4 GPUs | **Running** on `ghost-node-0` |
| `gpu-job-overflow` | 1 GPU | **Pending** |

Scheduler event on the overflow pod: `0/2 nodes are available: 1 Insufficient nvidia.com/gpu`.

The real kube-scheduler accounts for simulated GPU capacity, including exhaustion. Foundation confirmed.

## Q2 — Pod→GPU binding (blocker B1): **resolved, and asymmetric between paths**

**Legacy scalar path — binding must be re-derived.** Extended resources are scalar; nothing records *which* GPU a pod got, and with no kubelet there is no device plugin `Allocate()` and no podresources API. `fake-gpu-operator` solves this by re-deriving the binding centrally: a `status-updater` Deployment reads each pod's `nvidia.com/gpu` limit and greedily claims the first N unallocated GPUs in a per-node ConfigMap (`NodeTopology.Gpus[].Status.AllocatedBy`), with deterministic GPU IDs from `SHA1(nodeName-idx)`. Workable, but the ConfigMap is a hot object rewritten on every pod event — the obvious scaling ceiling.

**DRA path — binding is authoritative and free.** The scheduler writes the exact device into the claim:

```
dra-job-a-gpu-44pkc  -> driver=gpu.ghostgpu.dev pool=ghost-node-0 device=gpu-0  reservedFor=pods/dra-job-a
dra-job-b-gpu-kg42d  -> driver=gpu.ghostgpu.dev pool=ghost-node-0 device=gpu-1  reservedFor=pods/dra-job-b
```

**Consequence:** on the DRA path B1 does not exist, and no bespoke binding store is needed — `ResourceClaim.status` *is* the binding store. This removes the hot-object scaling problem by construction, and is the single biggest architectural finding of the spike.

## Q3 — DRA without a kubelet (blocker B2): **VIABLE**

`resource.k8s.io/v1` is **GA** on the tested cluster (k8s 1.36.1) — `deviceclasses`, `resourceclaims`, `resourceclaimtemplates`, `resourceslices` all present.

ResourceSlices were published by plain `kubectl apply` — no DRA kubelet plugin, no `NodePrepareResources`/`NodeUnprepareResources`. Results:

| Pod | Result | Claim state |
|---|---|---|
| `dra-job-a` | **Running** | `allocated,reserved` → `gpu-0` |
| `dra-job-b` | **Running** | `allocated,reserved` → `gpu-1` |
| `dra-job-overflow` | **Pending** | `pending` (only 2 devices exist) |

Allocation is performed by kube-scheduler and deallocation by kube-controller-manager's resourceclaim controller — neither requires a kubelet. `NodePrepareResources` is container setup only, which kwok skips because it drives pods to Running via Stages. B2 is closed as a risk.

## Q4 — MIG via DRA partitionable devices: **NATIVELY SUPPORTED** (unplanned finding)

`sharedCounters` + `device.consumesCounters` model overlapping MIG profiles drawing on one physical GPU's budget.

**API constraint discovered:** `sharedCounters` and `devices` **cannot coexist in one ResourceSlice** — the API rejects it with `only one of 'sharedCounters' or 'devices' is allowed`. They must be split across two slices of the same pool with `pool.resourceSliceCount: 2`.

With that split, modeling one H100 (budget: 80Gi memory, 7 slices) exposing overlapping `7g.80gb` and `1g.10gb` profiles:

| Step | Result |
|---|---|
| `mig-claim-big` requests `7g.80gb` | **allocated** `gpu-0-7g-80gb` (consumes entire budget) |
| `mig-claim-small` requests `1g.10gb` on same GPU | **pending** — correctly blocked |
| *Negative control:* release the big claim | `mig-job-small` → **Running**, allocated `gpu-0-1g-10gb` |

The negative control confirms counters — not some unrelated constraint — were the blocker. The upstream scheduler enforces MIG-style partitioning correctly with no custom logic from us.

**Consequence:** high-fidelity MIG simulation is achievable using stock upstream DRA. This is the capability `fake-gpu-operator` most clearly lacks (its `NodeTopology` has no MIG-instance field at all, no `GPU_I_ID` metrics, no MIG in ResourceSlices — its own design doc for this was never implemented).

## Impact on the design

1. **Invert the roadmap to DRA-first.** DRA was v0.4; it should be the primary path. It eliminates B1, needs no bespoke binding store, and unlocks MIG.
2. **Promote MIG.** It is our strongest confirmed differentiator and upstream does the hard part.
3. **Legacy scalar support becomes a compatibility layer**, not the foundation — it is the only path that needs greedy re-derivation, and it carries the scaling risk.
4. **B3 (fidelity/validation strategy) remains open** — it is a design decision, not a research question.

## Reproduction

Scratch manifests: `scratchpad/spike/{01..06}-*.yaml` (throwaway, not part of the repo).
Cluster teardown: `kwokctl delete cluster --name ghostgpu-spike`.
