# Metrics

The operator serves DCGM-shaped telemetry for the simulated fleet on port 9400 — dcgm-exporter's conventional port, so an existing scrape config or `ServiceMonitor` finds it unchanged:

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-8f3a…",modelName="NVIDIA H100 80GB HBM3",Hostname="ghost-0",namespace="team-a",pod="trainer",container="train"} 85
DCGM_FI_DEV_FB_USED{gpu="0",…,pod="trainer"} 57344
DCGM_FI_DEV_GPU_UTIL{gpu="1",…} 0
```

Metric and label names are taken from dcgm-exporter's default counter set rather than from memory, and pinned by a test, because dashboards and KEDA queries hardcode them exactly as tooling hardcodes GFD labels.

## The numbers are attributed

`namespace`, `pod`, `container` — and under MIG `GPU_I_ID` and `GPU_I_PROFILE` — come straight from `ResourceClaim.status`, which the scheduler wrote.

That is the payoff of the DRA-first design: there is nothing to re-derive from a container runtime, which is where real exporters accumulate bugs.

An idle GPU carries **no workload labels at all** rather than empty ones, because an empty `pod` label is a distinct series that `sum by (pod)` will happily group on.

## The numbers are declared, not randomised

A metric that jitters cannot be asserted against:

```yaml
spec:
  utilization:
    whenAllocated:
      gpuUtil: 85
      fbUsedPercent: 70
      powerWatts: 550
```

Unset fields default to fully busy when allocated and zero when idle. Framebuffer used and free are derived from the GPU's own memory, so they always sum to it.

**Power and temperature have no default** and are simply absent until declared — ghostgpu has no thermal or power model, and a plausible-looking wattage would be fabrication rather than simulation. Under MIG they stay at card level, matching dcgm-exporter: instances share one piece of silicon, so a per-instance wattage is a number no hardware could produce.

## Different jobs can report differently

This is what makes a fleet a useful fixture for idle-GPU reclamation and utilisation-based preemption. Those tools exist to work out *which* of several jobs is wasting its GPU, and a fleet where every allocated card reports the same number cannot ask that question.

```yaml
spec:
  utilization:
    whenAllocated:
      gpuUtil: 90                    # the fleet's well-behaved default
    workloads:
      - podSelector:
          matchLabels: {job: notebook}
        gpuUtil: 4                   # holding a GPU and barely using it
        fbUsedPercent: 95
```

Entries are first-match-wins and layer over `whenAllocated`, so one only has to say what makes that workload different. `matchExpressions` works too. Overrides apply only to *held* devices.

## One limitation worth knowing

Rules like `avg_over_time(DCGM_FI_DEV_GPU_UTIL[24h]) < 10` are computed by Prometheus from its own history, which ghostgpu cannot backfill. ghostgpu drives the current value; testing a long-window rule means shortening its window.

This is also why the behavioural phase timeline was deferred rather than built — see the [research](https://github.com/santimillang/ghostgpu/blob/main/docs/design/2026-07-31-behavioral-simulation-research.md).
