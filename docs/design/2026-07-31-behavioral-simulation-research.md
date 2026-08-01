# Behavioural workload simulation — research

**Date:** 2026-07-31
**Status:** research only; recommends splitting the roadmap item in two

The last unbuilt roadmap item is "behavioural workload simulation
(weight-download → warmup → training)". This is research into who would consume
it and what it has to do, before designing anything.

## Who consumes utilisation, concretely

Idle-GPU reclamation is a live and growing category, not a hypothetical. The
patterns in use are specific:

- `avg_over_time(DCGM_FI_DEV_GPU_UTIL[24h]) < 10` to surface GPUs that have been
  near-idle for a day
- utilisation below 20% for 30 minutes, *combined with* a large
  `DCGM_FI_DEV_FB_FREE` on an allocated pod, as an idle signal
- scheduler plugins that only permit preemption when a pod's measured
  utilisation is under a threshold, with a cooldown to avoid preemption storms

Sources: [CNCF, reclaiming underutilised GPUs with scheduler
plugins](https://www.cncf.io/blog/2026/01/20/reclaiming-underutilized-gpus-in-kubernetes-using-scheduler-plugins/),
[a custom scheduler plugin write-up](https://medium.com/@lalitlouis/reclaiming-idle-gpus-in-kubernetes-why-we-built-a-custom-scheduler-plugin-5ec2f7d13cc7),
[DevZero's measurement guide](https://www.devzero.io/guides/complete-guide-to-measuring-and-fixing-gpu-utilization).

## What real training telemetry looks like

Well-optimised training sits at **85–95%** during forward and backward passes,
with brief dips between batches for data loading. Utilisation drops during
checkpointing, and evaluation jobs spend a long stretch underutilised while
loading checkpoints and tokenising.

Notably, at least one large operator reports moving from `DCGM_FI_DEV_GPU_UTIL`
to `DCGM_FI_PROF_GR_ENGINE_ACTIVE` for a more precise view — ghostgpu already
publishes both, so that migration is testable against it.

Sources: [Introl on LLM training
throughput](https://introl.com/blog/gpu-performance-tuning-maximizing-throughput-llm-training-inference),
[Characterization of LLM Development in the Datacenter](https://arxiv.org/pdf/2403.07648),
[Lablup, 504-GPU pre-training operational analysis](https://arxiv.org/html/2605.09370v5).

## The finding that changes the design

**The scenarios people actually test have long time windows, and ghostgpu cannot
manufacture history.** `avg_over_time(...[24h])` is computed by Prometheus from
its own TSDB. ghostgpu drives the *current* value; it cannot backfill a day of
samples. So the headline use case cannot be tested by waiting, whatever phase
model gets built.

That is worth stating plainly because it is easy to build the phase machinery,
demo it convincingly, and never notice that the rule under test still needs its
window shortened to be exercised at all.

## The second finding: the valuable half needs no time axis

The reclamation scenario is about **heterogeneity, not motion**: one job is
wasting its GPU while another is using it properly, and the question is whether
the tool spots the right one.

ghostgpu's `spec.utilization` is currently per-*pool*, so every allocated GPU in
a pool reports identically. A fleet cannot contain a well-behaved job and a
wasteful one at the same time — which is exactly the fixture an idle-GPU
reclaimer needs, and it is unreachable today.

Making utilisation selectable per workload delivers that with no time axis, no
clock, and no tension with the determinism principle. It is a much smaller
change than phases and unblocks the named use case.

## Recommendation: split the item

**First — per-workload utilisation.** Let a pool declare readings that apply to
pods matching a selector, so a simulated fleet can hold a job at 90% next to one
at 4%. Small, deterministic, and it is what the reclamation and preemption
tooling actually needs to be pointed at.

**Then — phases, if still wanted.** Declared phases keyed to the *pod's age*, so
a reading stays a pure function of inputs and remains assertable, with a time
multiplier so a thirty-minute phase can be exercised in thirty seconds. Real
shapes (85–95% steady, dips at checkpoint boundaries) belong in documented
examples rather than in a built-in "training" preset: ghostgpu has no business
claiming a particular model's telemetry is faithful, the same reason the MIG
tables carry only NVIDIA's published numbers and the metrics carry no invented
thermal model.

Phases are the more impressive demo. Per-workload utilisation is the part that
makes a real tool testable, and it is a fraction of the work.

## Open questions for the phase design, if it happens

- **Where does a pod's clock come from?** `pod.Status.StartTime` is the obvious
  source and kwok sets it, but a restarted operator must derive the same phase
  from it — no in-memory timers.
- **Does a scrape-time-derived value break the "derive, don't store" rule?** No:
  the reading stays a pure function of (spec, pod age), which is as derivable as
  anything else the exporter computes.
- **Manual pinning** — an escape hatch to hold a pod in a named phase — may be
  worth more than the timeline itself for CI, where waiting is the enemy.
