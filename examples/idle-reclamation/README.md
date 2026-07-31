# Idle GPU reclamation

**The question.** Two jobs each hold a GPU. One is training; the other is a
notebook someone opened on Tuesday and forgot. Does my reclaimer pick the right
one?

This is the fixture that reclamation and utilisation-based preemption tooling
needs, and the awkward thing about building it for real is that you need two
jobs behaving differently on purpose — which usually means writing a program
that deliberately wastes a GPU.

## The fleet

One node, two GPUs, both allocated. The pool declares what each workload
reports:

```yaml
utilization:
  whenAllocated:
    gpuUtil: 92          # the well-behaved default
    fbUsedPercent: 78
  workloads:
    - podSelector:
        matchLabels: {workload: notebook}
      gpuUtil: 3         # holding a GPU, doing nothing with it
      fbUsedPercent: 91  # while sitting on most of its memory
```

The wasteful job holding *most of the framebuffer* while doing no compute is
deliberate: low utilisation combined with high memory use is what distinguishes
a squatting notebook from a job that is briefly between batches, and real idle
detection keys on the combination rather than utilisation alone.

## Run it

```sh
kubectl apply -f examples/idle-reclamation/
kubectl apply -f examples/idle-reclamation/jobs/
```

```console
$ curl -s ghostgpu-gpu-metrics.ghostgpu-system.svc:9400/metrics | grep GPU_UTIL
DCGM_FI_DEV_GPU_UTIL{gpu="0",...,pod="trainer"}   92
DCGM_FI_DEV_GPU_UTIL{gpu="1",...,pod="notebook"}   3
```

Both GPUs are allocated. Only one is being used. That is the whole scenario.

## What to point at it

- **A reclamation controller or scheduler plugin.** Does it evict the notebook
  and leave the trainer alone?
- **Showback and cost tooling.** Does the waste land on the right team's bill?
- **Alerting.** `DCGM_FI_DEV_GPU_UTIL < 10` with a high `DCGM_FI_DEV_FB_USED` on
  an allocated device.

## One limitation, stated up front

Real reclamation rules use long windows — `avg_over_time(DCGM_FI_DEV_GPU_UTIL[24h]) < 10`
is a common shape, as is "below 20% for 30 minutes". Prometheus computes those
from its own stored history, which ghostgpu cannot backfill: it drives the
*current* value, not the past.

So testing a 24-hour rule against this fixture means shortening the rule's
window for the test. That is a property of how Prometheus works rather than
something ghostgpu can fix, and it is better known now than discovered halfway
through writing a test.
