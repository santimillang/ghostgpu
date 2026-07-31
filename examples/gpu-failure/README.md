# GPU failure under a running job

**The question.** A GPU fails while a job is using it. Does my remediation
notice, drain the node, and get the work requeued somewhere healthy?

This is the scenario you cannot arrange on real hardware. There is no way to
ask a production GPU to fall off the bus so you can watch what your operator
does about it — which is why this failure path is so often untested until the
night it happens.

## The fleet

Two nodes, two GPUs each, all healthy to begin with.

## Run it

```sh
kubectl apply -f examples/gpu-failure/
kubectl apply -f examples/gpu-failure/jobs/trainer.yaml
```

Wait for the job to be running and holding a GPU:

```console
$ ghostgpu status --node ghost-fail-0
NODE          DEVICE  PROFILE  STATUS     POD
ghost-fail-0  gpu-0   -        allocated  default/trainer
ghost-fail-0  gpu-1   -        free       -
```

Now fail that node's GPUs:

```sh
kubectl patch gpupool failure-demo --type=merge -p '{"spec":{"faults":[{
  "nodeSelector": {"kubernetes.io/hostname": "ghost-fail-0"},
  "gpus": 2, "effect": "Evict", "xid": 79
}]}}'
```

XID 79 is "GPU has fallen off the bus" — the canonical hard failure. The fault
covers both of that node's GPUs rather than one, because the claim did not name
a device: the scheduler chose, so failing only `gpu-0` might miss the card the
job actually holds. `ghost-fail-1` stays healthy, which is where the work
should end up.

Two things happen, and both matter:

```console
$ kubectl get pod trainer
Error from server (NotFound): pods "trainer" not found

$ ghostgpu status --node ghost-fail-0
NODE          DEVICE  PROFILE  STATUS   POD
ghost-fail-0  gpu-0   -        faulted  (failed)
ghost-fail-0  gpu-1   -        faulted  (failed)
```

The job was **evicted**, and its `ResourceClaim` was **released** — so it is
free to be rescheduled onto healthy hardware rather than stuck holding a
device that no longer works. The eviction is performed by upstream Kubernetes
through a DRA device taint; ghostgpu contributes no eviction logic of its own.

The failure also shows up where remediation actually looks:

```console
$ curl -s ghostgpu-gpu-metrics.ghostgpu-system.svc:9400/metrics | grep XID
DCGM_FI_DEV_XID_ERRORS{gpu="0",Hostname="ghost-fail-0",...} 79
DCGM_FI_DEV_XID_ERRORS{gpu="1",Hostname="ghost-fail-0",...} 0
```

## Repair it

```sh
kubectl patch gpupool failure-demo --type=json -p='[{"op":"remove","path":"/spec/faults"}]'
```

The GPU returns to service and pending work schedules onto it. Being able to
repair a fleet is what lets you test the *recovery* half, which is usually the
half with the bugs.

## What to point at it

- **A node problem detector or health controller.** Does it act on the XID?
- **A remediation operator.** Does it cordon, drain, or open a ticket?
- **A queueing system** (Kueue, Volcano). Does the evicted work get requeued,
  or silently lost?
- **Alerting rules.** Does anyone find out?

## Other failure modes

`effect: Unschedulable` models a card that still runs but must take no new
work — a row remap pending a reboot, say. Work already on it is left alone,
which is the drain-before-maintenance case rather than the sudden-death one.
