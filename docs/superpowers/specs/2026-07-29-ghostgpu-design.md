# ghostgpu — Design Spec

**Date:** 2026-07-29
**Status:** Revised after feasibility spike (see `2026-07-29-spike-findings.md`). Pending user review.
**License:** Apache-2.0
**Publish target:** github.com/santimillang/ghostgpu (private during early development)

## 1. Summary

ghostgpu simulates GPU clusters on Kubernetes so that GPU-aware schedulers, autoscalers, and platform tooling can be tested with **zero GPU hardware**. It builds on [kwok](https://kwok.sigs.k8s.io/) (CNCF sandbox) for node/pod simulation and adds the GPU layer: DRA-native device topology, MIG partitioning, DCGM-shaped metrics, a behavioral workload execution engine, and hardware fault injection.

Primary users: maintainers of GPU-scheduling projects (Kueue, Volcano, KAI Scheduler, HAMi) who need CI coverage without burning GPU-hours, and platform engineers validating scheduling and autoscaling policy before touching production.

## 2. Prior art and positioning

| Project | Covers | Gap |
|---|---|---|
| **kwok** (CNCF sandbox) | Generic fake nodes/pods, arbitrary node resources | Nothing GPU-specific |
| **`fake-gpu-operator`** (github.com/run-ai/fake-gpu-operator; actively maintained, ~biweekly releases) | kwok-based; dynamic GPU-utilization metrics; DRA ResourceSlice publication; node capacity patching | **No fault injection**; **no behavioral pod-phase simulation**; **no MIG-instance state** (its `NodeTopology` has no MIG field, no `GPU_I_ID` metrics, no MIG in ResourceSlices — its own MIG design doc was never implemented); **podresources explicitly uncovered on the kwok path** (their docs: "a single central pod cannot serve one kubelet socket per virtual node") |
| Kueue, Volcano, KAI Scheduler, HAMi, KAITO, llm-d, KubeRay, Karpenter, KEDA, KServe | Production scheduling/routing/autoscaling | Not simulators — these are the systems ghostgpu exists to test |

**Differentiators, in confidence order:** (1) MIG-instance fidelity, (2) fault injection, (3) behavioral workload simulation, (4) scale architecture that avoids a central hot object, (5) podresources on the kwok path.

**Honesty constraint:** capacity advertising, dynamic metrics, and basic DRA are *table stakes*, already shipped by `fake-gpu-operator`. Public copy must credit it accurately and claim novelty only where we have it.

⚠️ **Licensing:** `fake-gpu-operator`'s `LICENSE` is MIT, its README claims Apache-2.0, and `NOTICE` carries an NVIDIA copyright. We may learn from its approach; we must **not vendor its code** without resolving that discrepancy upstream.

## 3. Key architectural decision: DRA-first

The spike established that Dynamic Resource Allocation works fully without a kubelet, and that **the scheduler records exact device identity** in `ResourceClaim.status.allocation` (verified: `dra-job-a → gpu-0`, `dra-job-b → gpu-1`).

This is decisive:

- **The pod→GPU binding problem disappears on the DRA path.** `ResourceClaim.status` *is* the binding store. No bespoke state, no greedy re-derivation, no central hot object.
- `fake-gpu-operator` needs a per-node ConfigMap rewritten on every pod event precisely because the legacy scalar path cannot express which GPU was assigned. We avoid that class of problem by construction.
- MIG is expressible natively via DRA partitionable devices (`sharedCounters` / `consumesCounters`), enforced by the upstream scheduler — verified in the spike, including a negative control.

Legacy scalar advertising (`nvidia.com/gpu: 8`) remains supported — it is a single node-status patch and costs almost nothing — but it is a **compatibility layer**, not the foundation. Only that path needs binding re-derivation, and it is deferred accordingly.

## 4. Architecture

Single operator binary (kubebuilder/controller-runtime), plus a metrics exporter and a CLI.

1. **`gpupool-controller`** — reconciles `GPUModel`/`GPUPool`. Publishes DRA `ResourceSlice`s (device per simulated GPU, attributes for product/UUID/NVLink domain/NUMA) and patches kwok node `status.capacity`/`allocatable` for legacy consumers. Stamps GPU Feature Discovery-compatible node labels.
2. **`mig-controller`** — expands a pool into MIG profiles as partitionable devices: a counter-set slice holding the physical GPU budget plus a device slice of overlapping profiles that consume it. Upstream enforces exclusivity.
3. **`ghostgpu-exporter`** — serves DCGM-shaped Prometheus metrics (`DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_POWER_USAGE`, plus `GPU_I_ID`/`GPU_I_PROFILE` for MIG). Attribution is derived by **watching `ResourceClaim.status`** — no separate mapping store.
4. **`workload-controller`** — reconciles `GPUSimulationProfile`. Drives pods through a phase state machine (weight-download → warmup → training), feeding phase-correlated metrics and optionally failing the pod (OOM/exit 137, or XID).
5. **`fault-controller`** — reconciles `GPUFaultScenario`: injects XID/ECC/thermal/device-loss faults against pools on a schedule, marks devices unhealthy (device taints in DRA), and emits realistic Events. Independent of any workload.
6. **`ghostgpu` CLI** — quickstart wrapper so adoption isn't gated on hand-writing CRs: `ghostgpu up --nodes 100 --gpu h100 --gpus-per-node 8`, `ghostgpu advance --duration 10m`, `ghostgpu scenario apply <file>`.

`workload-controller` covers workload-triggered failures; `fault-controller` covers cluster-triggered ones.

## 5. Cross-cutting design requirements

**Virtual clock (required for CI usability).** A 5-minute training phase must not cost 5 minutes of wall time. Every duration in every CRD is interpreted through a simulated clock, configured operator-wide (Helm value / `--time-scale` flag, surfaced in a `GhostGPUConfig` singleton CR so it is inspectable at runtime):
- `timeScale: 60` → all durations run 60× faster; a 5m phase completes in 5s.
- `timeScale: 0` → stepped mode: simulated time only advances via `ghostgpu advance --duration 10m`, giving tests exact control with no sleeps.

It is operator-wide rather than per-CR so that a fault scheduled at `after: 10m` and a workload phase of `5m` stay on one coherent timeline. Without this the behavioral engine is unusable in CI.

**Determinism.** All stochastic behavior (`failureRate`, greedy assignment order) draws from a seeded PRNG. Identical seed + identical input ⇒ identical outcome. A `deterministic` mode disables wall-clock-derived behavior entirely. Flaky simulators do not get adopted for CI.

**Scale targets (measurable, tested in CI).** 1,000 fake nodes × 8 GPUs (8,000 devices) on a 16 GB developer laptop; steady-state reconcile p99 < 5 s; no single object rewritten on every pod event.
Known API limit discovered in the spike: **128 devices per ResourceSlice, 64 if devices use counters or taints.** MIG expansion multiplies device count (8 GPUs × 7 profiles = 56 devices — fits, but larger pools must shard across slices). The sharding strategy is an explicit v0.2 design item.

**Safety.** The operator must never touch real infrastructure: it refuses to modify any Node lacking the kwok annotation, ships least-privilege RBAC, and aborts on startup if real kubelets are detected in-cluster (overridable only by explicit flag). `SECURITY.md` documents this as the threat model, not just a reporting address.

**Fidelity contract (blocker B3 — the trust problem).** A simulator is worth only what its fidelity claims are worth, so fidelity is a documented, tested artifact with three tiers:
- **Faithful** — object shapes and label sets are byte-comparable to real ones: ResourceSlice/ResourceClaim semantics, DCGM metric names and label sets, GFD node labels, MIG profile→GI-ID tables.
- **Approximated** — metric value curves, phase timings, fault propagation delays. Plausible, not measured-from-hardware.
  - **MIG on the legacy extended-resource path** belongs here, for a structural reason rather than as a shortcut (recorded 2026-07-30, v0.2). Scalar extended resources cannot express that profiles on one physical GPU are mutually exclusive, so a node advertising both `nvidia.com/mig-1g.10gb` and `nvidia.com/mig-7g.80gb` lets a scheduler allocate from both at once — something real hardware could not satisfy. Each per-profile count is faithful on its own (derived from both budgets, matching NVIDIA's published instance tables), but their *sum* overcommits. The DRA path models the exclusion correctly through shared counters and is the faithful one; the mixed-strategy projection exists for tooling that predates DRA. This is the same limitation that stopped scalar resources being ghostgpu's primary path in v0.1.
- **Not simulated** — actual CUDA execution, real interconnect bandwidth/latency, driver internals, thermals as physics.

Enforced by golden-file conformance tests comparing emitted objects and metric payloads against captured real-world samples, plus a published "known divergences" page. Interconnect *bandwidth* remains explicitly out of scope: no scheduler consumes it today, and modeling it would balloon complexity for no testing value. Topology *structure* (NVLink domain membership, NUMA locality) is modeled, because schedulers do consume that.

## 6. CRD API (sketch)

All API objects carry a `status` subresource with observed state and conditions.
Scoping: `GPUModel`, `GPUPool`, `GPUFaultScenario` are **cluster-scoped**; `GPUSimulationProfile` is **namespaced**.

```yaml
apiVersion: ghostgpu.dev/v1alpha1
kind: GPUModel                      # cluster-scoped: a hardware archetype
spec:
  vendor: nvidia
  productName: "NVIDIA-H100-SXM"
  memory: 80Gi
  computeCapability: "9.0"
  migProfiles:                      # budget-based, mirrors DRA counters
    - name: "1g.10gb"
      consumes: {memory: 10Gi, slices: 1}
    - name: "3g.40gb"
      consumes: {memory: 40Gi, slices: 3}
    - name: "7g.80gb"
      consumes: {memory: 80Gi, slices: 7}
  migBudget: {memory: 80Gi, slices: 7}
---
apiVersion: ghostgpu.dev/v1alpha1
kind: GPUPool                       # cluster-scoped
spec:
  modelRef: NVIDIA-H100-SXM
  nodeSelector: {type: kwok}
  gpusPerNode: 8                    # renamed from ambiguous `count`
  advertise:
    dra: true                       # primary path
    extendedResource: true          # legacy compat
  sharingMode: mig                  # none | mig | timeSlicing
  topology:
    nvlinkDomainSize: 4             # 8 GPUs -> 2 domains of 4
    numaAware: true
---
apiVersion: ghostgpu.dev/v1alpha1
kind: GPUSimulationProfile          # namespaced
# Bound to pods via annotation: ghostgpu.dev/profile: <name>
spec:
  seed: 1337                        # determinism
  phases:
    - name: initializing            # simulates weight download
      duration: 45s
      utilization: {gpu: 0, memoryPattern: linear, target: 70Gi}
    - name: warmup                  # CUDA context init
      duration: 10s
      utilization: {gpu: 30, memoryPattern: hold}
    - name: training
      duration: 5m
      utilization: {gpu: 95, memoryPattern: burst}
  failure:
    rate: 0.05
    mode: oom                       # oom (exit 137) | xid
---
apiVersion: ghostgpu.dev/v1alpha1
kind: GPUFaultScenario              # cluster-scoped
spec:
  targetPoolRef: h100-pool
  seed: 42
  trigger: {after: 10m}             # simulated time, honors timeScale
  fault: {type: XidError, code: 79, affectedGPUs: 2}
```

## 7. Data flow

1. Install kwok, then ghostgpu (Helm chart) — or just `ghostgpu up` for the batteries-included path.
2. Apply `GPUModel` + `GPUPool` → `gpupool-controller` publishes ResourceSlices (and legacy node capacity); `mig-controller` expands MIG profiles into counter-backed partitionable devices.
3. `ghostgpu-exporter` serves baseline DCGM-shaped metrics.
4. Deploy the real system under test (Kueue, Volcano, KEDA, a custom operator) exactly as against real hardware. The upstream scheduler allocates devices and records identity in `ResourceClaim.status`.
5. Optional: pods annotated with a `GPUSimulationProfile` are driven through phases, with metrics tracking phase and probabilistic failures.
6. Optional: a `GPUFaultScenario` injects hardware faults to exercise the system's resilience paths.

## 8. Success criteria

**v1.0 ships when all of the following hold:**
- A real, pinned Kueue **and** Volcano release schedule GPU workloads correctly against a ghostgpu cluster in CI, including quota exhaustion and gang-scheduling cases.
- MIG overlapping-profile exclusivity is enforced and covered by tests (including the negative control from the spike).
- A KEDA scaler driven by ghostgpu-emitted DCGM metrics scales a workload end-to-end in CI.
- A fault scenario causes a system under test to drain/reschedule, asserted in CI.
- 1,000-node scale target met on documented hardware, measured in CI.
- Fidelity contract published, with golden-file conformance tests green.
- Docs site live; `ghostgpu up` produces a working cluster in under 2 minutes on a clean machine.

**Explicit non-goals:** real CUDA execution; interconnect bandwidth/latency simulation; being a production scheduler, admission controller, or monitoring system; replacing kwok; multi-vendor parity in v1 (NVIDIA-first — AMD/Intel conventions are post-v1).

## 9. Testing & repo tooling

- Go, kubebuilder, controller-runtime; envtest for controller unit tests; golden-file tests for the fidelity contract.
- e2e: kind + kwok + ghostgpu against pinned real releases of Kueue/Volcano/KEDA. **Upstream-churn strategy:** versions are pinned in a single manifest, with a scheduled CI job that tests against latest upstream and opens an issue on divergence — so breakage surfaces as a tracked signal rather than silent rot.
- Coexistence: detect a real device plugin or `fake-gpu-operator` in-cluster and refuse to co-manage the same nodes, with a clear error.
- Repo hygiene: `LICENSE` (Apache-2.0), **DCO sign-off** (required for any future CNCF donation — cheap now, painful to retrofit), `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, GitHub Actions (lint, unit, e2e, multi-arch images), goreleaser, Helm chart, mkdocs site.

## 10. Roadmap

- **v0.1** — `GPUModel`/`GPUPool`; DRA ResourceSlice publication + legacy capacity patching; GFD-compatible labels; `ghostgpu up` CLI.
- **v0.2** — `mig-controller`: partitionable devices, counter-set sharding strategy. *(First real differentiator.)*
- **v0.3** — `ghostgpu-exporter`: DCGM-shaped metrics with claim-derived attribution, including MIG `GPU_I_ID`.
- **v0.4** — `workload-controller`: behavioral phase engine, virtual clock, determinism.
- **v0.5** — `fault-controller`: fault injection and device taints.
- **v0.6** — Legacy-path completeness: scalar binding re-derivation + podresources shim for the kwok path.
- **v1.0** — Hardening against §8 success criteria; scale validation; docs site; CNCF sandbox application.

## 11. Open items

- **API group name — decide before first public release, not before coding.** `ghostgpu.dev/v1alpha1` is used throughout. An API group is only a DNS-subdomain-*formatted* string; nothing resolves or validates it, so domain ownership is a collision-avoidance convention, not a requirement. Precedent: kwok's group is `kwok.x-k8s.io` and its docs are at `kwok.sigs.k8s.io` — both under Kubernetes-owned domains its maintainers never registered. If ghostgpu ever enters k8s-sigs/CNCF the group would likely become `ghostgpu.x-k8s.io` anyway.
  While the project is unreleased, changing the group costs a find-and-replace plus `make generate`. Once users hold `ghostgpu.dev/v1alpha1` objects in real clusters it becomes a breaking change requiring conversion webhooks. **Therefore: proceed on `ghostgpu.dev` now; revisit at the v0.1 public-release gate.** Registering the domain is worthwhile for the brand and docs site, but that is a separate decision from the API group.
- Exporter deployment topology: one Deployment serving all nodes (scales, less realistic scrape behavior) vs. per-node (realistic, heavier). Leaning single multi-node exporter, matching how `fake-gpu-operator` handles the kwok path; revisit against the 1,000-node target.
- Minimum supported Kubernetes version. Spike ran on 1.36.1 where `resource.k8s.io/v1` is GA; supporting older releases means handling `v1beta1` shape differences.
- Governance path: a personal repo is fine now, but CNCF sandbox requires a neutral home — decide before the v1.0 application.
