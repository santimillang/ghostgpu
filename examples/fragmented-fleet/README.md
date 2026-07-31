# Fragmented fleet

**The question.** Seven GPUs are free across the cluster, but they are spread
2/2/2/1 across four nodes. Does my four-GPU job schedule? Should it? Does my
autoscaler add a node, and is that the right call?

This is the shape most GPU scheduling bugs actually take. It is also the one
that is hardest to build by hand: submitting filler pods and hoping they land
where you intended is racy, and where they land is the scheduler's choice — so
the scenario under test is never quite the one you meant to test.

## The fleet

Four nodes, four GPUs each, unevenly occupied:

| Node | Busy | Free |
| --- | --- | --- |
| `ghost-rack-a0` | 2 | 2 |
| `ghost-rack-a1` | 2 | 2 |
| `ghost-rack-b0` | 2 | 2 |
| `ghost-rack-c0` | 3 | 1 |

Seven free out of sixteen, and no node with more than two.

## Run it

```sh
kubectl apply -f examples/fragmented-fleet/
```

```console
$ ghostgpu status
POOL           MODE  NODES  DEVICES  FAULTED  OCCUPIED  ALLOCATED  FREE
fragmented     none  4      16       0        9         0          7
```

The workloads live in `jobs/`, which `kubectl apply -f` does not descend into,
so the fleet comes up before anything is submitted against it. Now ask for four
GPUs on one node:

```sh
kubectl apply -f examples/fragmented-fleet/jobs/four-gpu.yaml
```

It stays **Pending**. Seven GPUs are free and it cannot have four of them:

```console
$ kubectl get pod frag-4gpu
NAME        READY   STATUS    RESTARTS   AGE
frag-4gpu   0/1     Pending   0          30s
```

A two-GPU job placed alongside it schedules immediately, which is what makes
this a fragmentation problem rather than a capacity one.

## What to point at it

- **A cluster autoscaler.** Should it add a node? The cluster has the capacity
  in aggregate; no single node does. Getting this wrong is expensive in both
  directions.
- **A gang or queueing system** (Kueue, Volcano). Does it hold the job, admit
  it partially, or wedge?
- **A bin-packing or defragmentation controller.** Does it consolidate?

## Change the shape

Edit `pool.yaml` and re-apply — the fleet reshapes without recreating anything:

```yaml
occupancy:
  - nodeSelector: {rack: c}
    busyPerNode: 3
  - busyPerNode: 2      # first-match-wins; this is the default
```

Set `busyPerNode: 0` for rack `a` and the pending four-GPU job schedules, which
is a useful way to check your tooling reacts to capacity appearing rather than
only to pods arriving.
