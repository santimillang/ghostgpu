# ghostgpu

[![Tests](https://github.com/santimillang/ghostgpu/actions/workflows/test.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/test.yml)
[![Lint](https://github.com/santimillang/ghostgpu/actions/workflows/lint.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/lint.yml)
[![E2E](https://github.com/santimillang/ghostgpu/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/santimillang/ghostgpu/actions/workflows/test-e2e.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/santimillang/ghostgpu)](https://goreportcard.com/report/github.com/santimillang/ghostgpu)
[![Go Reference](https://pkg.go.dev/badge/github.com/santimillang/ghostgpu.svg)](https://pkg.go.dev/github.com/santimillang/ghostgpu)
[![Go Version](https://img.shields.io/github/go-mod/go-version/santimillang/ghostgpu)](go.mod)
[![Release](https://img.shields.io/github/v/release/santimillang/ghostgpu?include_prereleases&sort=semver&label=release)](https://github.com/santimillang/ghostgpu/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![DCO](https://img.shields.io/badge/DCO-required-brightgreen.svg)](https://developercertificate.org/)

Simulate GPU clusters on Kubernetes. Test GPU-aware schedulers, autoscalers, and platform tooling with **zero GPU hardware**.

ghostgpu builds on [kwok](https://kwok.sigs.k8s.io/) and publishes Dynamic Resource Allocation (DRA) `ResourceSlice`s plus legacy extended-resource capacity, so a real `kube-scheduler` makes real placement decisions against hardware that does not exist.

> **Status:** early development. The core works and is covered end-to-end against a real `kube-scheduler`, but the `v1alpha1` API carries no compatibility guarantee yet.

## Install

Helm:

```sh
helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --namespace ghostgpu-system --create-namespace
```

Or a single manifest:

```sh
kubectl apply -f https://github.com/santimillang/ghostgpu/releases/latest/download/install.yaml
```

The CLI is a separate binary — download it from
[the latest release](https://github.com/santimillang/ghostgpu/releases/latest),
or build it with `make build-cli`.

Installing is safe alongside real hardware: ghostgpu only ever modifies nodes carrying kwok's `kwok.x-k8s.io/node` annotation, whatever a pool selector matches.

## Why

Testing GPU scheduling logic normally requires GPUs — expensive to idle, slow to provision, and impractical in CI. ghostgpu lets you run the real thing (Kueue, Volcano, KEDA, your own operator) against simulated fleets on a laptop — including [a copy of the fleet you actually run](#reproducing-a-real-cluster), read out of your own cluster.

Already verified against a real `kube-scheduler`: pods are placed against simulated GPU capacity and correctly refused once that capacity is exhausted — including MIG-style partitioning, where overlapping profiles on the same physical GPU are mutually exclusive.

## Quickstart

Needs [`kwokctl`](https://kwok.sigs.k8s.io/docs/user/installation/), `kind`, Docker, and Go.

```sh
# 1. A kwok cluster with DRA enabled
kwokctl create cluster --name ghostgpu --runtime kind \
  --kube-feature-gates "DynamicResourceAllocation=true" \
  --kube-runtime-config "resource.k8s.io/v1=true"

# 2. Install ghostgpu
kubectl apply -f https://github.com/santimillang/ghostgpu/releases/latest/download/install.yaml

# 3. Give your kwok nodes some GPUs
curl -sSfL https://github.com/santimillang/ghostgpu/releases/latest/download/ghostgpu_linux_amd64.tar.gz \
  | tar xz ghostgpu
./ghostgpu up --gpus-per-node 8 --nvlink-domain-size 4
```

```
gpumodel/h100 created
gpupool/h100-pool created
simulating 16 GPUs across 2 nodes
```

Each node now advertises `nvidia.com/gpu: 8`, GPU Feature Discovery labels, and a DRA `ResourceSlice` whose devices carry product, UUID, and NVLink-domain attributes. Pods scheduling against them are placed by the real scheduler, and refused when the simulated capacity runs out.

`--dry-run` prints the manifests instead of applying them, and contacts no cluster:

```sh
./bin/ghostgpu up --gpu NVIDIA-A100-SXM4-40GB --memory 40Gi \
  --compute-capability 8.0 --dry-run | kubectl apply -f -
```

### MIG

Partition each GPU into MIG instances and let the scheduler enforce that overlapping profiles on one card are mutually exclusive:

```sh
./bin/ghostgpu up --gpu NVIDIA-H100-80GB-HBM3 --sharing-mode mig --gpus-per-node 16
```

Profiles come from built-in tables matching NVIDIA's published instance counts, so an H100 offers seven `1g.10gb` per card but only four `1g.20gb` — memory binds before compute slices do. `--mig-profiles 1g.10gb,3g.40gb` restricts a pool to a subset.

Exclusivity is enforced by the upstream scheduler through DRA shared counters; ghostgpu contributes no allocation logic.

By default this models **dynamic MIG**, as NVIDIA's DRA driver does: every profile is offered and the scheduler picks. To model **static MIG**, where an administrator pre-created the instances, declare them:

```sh
./bin/ghostgpu up --gpu NVIDIA-H100-80GB-HBM3 --sharing-mode mig \
  --mig-partition 3g.40gb=1,1g.10gb=4
```

The distinction matters for the legacy extended-resource projection (`nvidia.com/mig-1g.10gb` and friends, NVIDIA's `mixed` strategy). Scalar resources cannot say "these two are the same silicon", so under dynamic MIG a node advertises alternatives whose *sum* no card could satisfy — each count is right, their total is not. **A declared partition makes that projection exact**, because the declared instances all coexist. The DRA path is faithful either way. See the fidelity contract in the design spec and [#28](https://github.com/santimillang/ghostgpu/issues/28).

### Injecting hardware failures

Hardware failure is the hardest thing to test for, because you cannot arrange it on demand — there is no way to ask a production GPU to fall off the bus so you can see whether your remediation drains the node. Declare it instead:

```yaml
spec:
  faults:
    - nodeSelector: {rack: a}
      gpus: 1
      effect: Evict        # the card is gone; the job must move
      xid: 79              # reported on DCGM_FI_DEV_XID_ERRORS
```

`Evict` models device loss and uncorrectable ECC. The workload running on that GPU is **thrown off and its `ResourceClaim` released**, so it can be rescheduled onto healthy hardware — which is the behaviour a remediation or requeueing system under test actually needs to exercise. `Unschedulable` models a card that still runs but must take no new work, such as one with a row remap pending a reboot.

The `xid` surfaces on `DCGM_FI_DEV_XID_ERRORS`, which is the signal most remediation watches, and rides along on the device taint so `kubectl get resourceslice` explains why a device is out of service without anyone scraping metrics.

Faults and occupancy are independent declarations applied lowest-index-first, and a fault wins where they overlap — so "three busy, one faulted" means the failure happened to a GPU that was working. Repairing a fleet is just removing the entry, which makes a pending job schedulable again.

### Metrics

The operator serves DCGM-shaped telemetry for the simulated fleet on port 9400 — dcgm-exporter's conventional port, so an existing scrape config or ServiceMonitor finds it unchanged:

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-8f3a…",modelName="NVIDIA H100 80GB HBM3",Hostname="ghost-0",namespace="team-a",pod="trainer",container="train"} 85
DCGM_FI_DEV_FB_USED{gpu="0",…,pod="trainer"} 57344
DCGM_FI_DEV_GPU_UTIL{gpu="1",…} 0
```

**The numbers are attributed.** `namespace`, `pod`, `container` — and under MIG `GPU_I_ID` and `GPU_I_PROFILE` — come straight from `ResourceClaim.status`, which the scheduler wrote. That is the payoff of the DRA-first design: there is nothing to re-derive from a container runtime, which is where real exporters accumulate bugs. An idle GPU carries no workload labels at all rather than empty ones, because an empty `pod` label is a distinct series that `sum by (pod)` will happily group on.

**The numbers are declared, not randomised**, because a metric that jitters cannot be asserted against:

```yaml
spec:
  utilization:
    whenAllocated:
      gpuUtil: 85
      fbUsedPercent: 70
      powerWatts: 550
```

Unset fields default to fully busy when allocated and zero when idle. Power and temperature have no default and are simply absent until declared — ghostgpu has no thermal or power model, and a plausible-looking wattage would be fabrication rather than simulation.

**Different jobs can report differently**, which is what makes a fleet a useful fixture for idle-GPU reclamation and utilisation-based preemption: those tools exist to work out *which* of several jobs is wasting its GPU, and a fleet where every allocated card reports the same number cannot ask that question.

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

Entries are first-match-wins and layer over `whenAllocated`, so one only has to say what makes that workload different. `matchExpressions` works too.

One limitation worth knowing before you build a test around this: rules like `avg_over_time(DCGM_FI_DEV_GPU_UTIL[24h]) < 10` are computed by Prometheus from its own history, which ghostgpu cannot backfill. ghostgpu drives the current value; testing a long-window rule means shortening its window.

### Starting from a full cluster

Most interesting GPU scheduling bugs are about fragmentation rather than capacity: *seven GPUs are free, but spread 2/2/2/1 across four nodes — does my four-GPU job schedule? Should it? Does my autoscaler add a node, and is that the right call?*

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

Occupied GPUs are still published — a busy fleet has the same hardware as an idle one — but they carry a DRA device taint, so the upstream scheduler will not allocate them, and the legacy path advertises `allocatable` below `capacity`. ghostgpu never writes `ResourceClaim.status`: allocation is state the scheduler owns, and forging it would make the simulation lie to the very component under test. Lifting the occupancy releases the devices, so a pending job can be made schedulable mid-test.

Under `sharingMode: mig`, `busyPerNode` still counts whole cards and every instance carved from an occupied one becomes unavailable. That is the only correct reading: MIG instances draw on shared counters, so leaving one profile allocatable would mean the card was never occupied.

### Reproducing a real cluster

Describing your fleet by hand is guesswork about exactly the details that matter — the ones that differ from the defaults are usually the ones causing the bug you are chasing. `ghostgpu capture` reads a cluster that already has GPUs and prints the manifests that reproduce it:

```sh
ghostgpu capture --context prod-us-east > fleet.yaml
ghostgpu capture --context prod-us-east | kubectl apply -f -
```

Everything comes from what such a cluster already publishes: GFD labels give the product, memory, and compute capability; `nvidia.com/mig-*` capacity gives the per-GPU MIG layout; `ResourceSlice` attributes give NVLink domains and NUMA locality. Distinct node shapes become distinct pools, and the kwok `Node` manifests come with them, so applying the output is enough to have the fleet — `--nodes=false` prints only the pools.

**It only ever reads.** The client is narrowed to a read-only type before the command is handed one, so there is no create, update, or delete for it to call; pointing a simulator at production has to be provably harmless, not merely intended to be. The manifests go to stdout, so applying them stays your own explicit act, and node names are synthesised rather than copied so captured output is safe to paste into an issue.

Capture is lossy by design: it reproduces *shape*, not workloads. Anything it cannot reproduce faithfully — a non-uniform MIG layout, a node whose GFD labels are incomplete, the `single` MIG strategy — is reported on stderr, so `> fleet.yaml` still yields a clean file.

### Seeing what happened

Scheduling is the point, so ghostgpu can tell you what the scheduler did without hand-writing jsonpath against `ResourceClaim`s:

```sh
$ ghostgpu status
POOL       MODE  NODES  DEVICES  OCCUPIED  ALLOCATED  FREE
h100-pool  mig   2      40       10        1          29

$ ghostgpu status --node ghost-mig-0
NODE         DEVICE           PROFILE  STATUS     POD
ghost-mig-0  gpu-0-1g-10gb-0  1g.10gb  free       -
ghost-mig-0  gpu-0-3g-40gb-0  3g.40gb  allocated  default/trainer
ghost-mig-0  gpu-1-1g-10gb-0  1g.10gb  occupied   (declared)

$ ghostgpu status --budgets --node ghost-mig-0
NODE         GPU    SLICES  MEMORY
ghost-mig-0  gpu-0  3/7     40Gi/80Gi
ghost-mig-0  gpu-1  0/7     0/80Gi
```

The budget view answers the question that is genuinely tedious to work out by hand: how much of a physical GPU is already spoken for, and therefore why another MIG instance will not fit. Everything is derived from objects already in the cluster — ghostgpu stores no allocation state of its own.

ghostgpu only ever modifies nodes carrying kwok's `kwok.x-k8s.io/node` annotation. A node without it is never touched, whatever the pool selector matches — see [SECURITY.md](SECURITY.md).

## Scenarios

[`examples/`](examples) holds worked scenarios, each answering a question you
might need to ask of your own tooling — a [fragmented fleet](examples/fragmented-fleet)
that refuses a four-GPU job while seven GPUs are free, a [GPU failing under a
running job](examples/gpu-failure), an [idle notebook squatting on a card](examples/idle-reclamation),
and [MIG exclusivity](examples/mig-exclusivity). All of them are applied and
checked by CI, so a scenario that stops working fails the build.

## Capabilities

| Area | Status |
|---|---|
| DRA `ResourceSlice` publication + legacy `nvidia.com/gpu` capacity + GFD labels | working, unreleased (v0.1) |
| MIG / partitionable devices | working, unreleased (v0.2) |
| Pre-existing occupancy / fragmentation scenarios | working, unreleased |
| DCGM-shaped Prometheus metrics with per-pod and per-MIG-instance attribution | working, unreleased (v0.3) |
| GPU fault injection (XID, device loss, drain-before-reboot) | working, unreleased |
| Behavioral workload simulation (weight-download → warmup → training) | planned |

## Prior art

[`fake-gpu-operator`](https://github.com/run-ai/fake-gpu-operator) is an actively maintained project covering capacity advertising, dynamic GPU-utilization metrics, and basic DRA on kwok. If that is all you need, use it.

ghostgpu differentiates on **MIG-instance fidelity**, **fault injection**, and **behavioral workload simulation** — none of which `fake-gpu-operator` currently provides.

## Development

Requires Linux (or WSL2 on Windows): `kubebuilder` ships no Windows binary, and `envtest` needs a Linux `kube-apiserver`.

```sh
make manifests generate   # regenerate CRDs and deepcopy code
make build                # build the manager
make build-cli            # build the ghostgpu CLI
make test                 # unit tests + envtest
make test-e2e             # e2e against kwok + kind
```

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, testing layers, and PR process.

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Governance](GOVERNANCE.md) · [Maintainers](MAINTAINERS.md)
- [Security policy and threat model](SECURITY.md)
- [Changelog](CHANGELOG.md)

## License

Apache-2.0 — see [LICENSE](LICENSE). Contributions require [DCO](https://developercertificate.org/) sign-off (`git commit -s`).
