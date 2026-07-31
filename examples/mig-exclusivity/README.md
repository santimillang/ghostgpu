# MIG exclusivity

**The question.** A card is offered as several overlapping MIG profiles. Does my
scheduler understand that allocating a `7g.80gb` makes every other profile on
that card unavailable?

Getting this wrong means a scheduler that cheerfully places two workloads on
silicon that can only hold one. It is difficult to test without MIG hardware,
and difficult to test *safely* with it.

## The fleet

One node with a **single** H100, offered as the six profiles NVIDIA publishes
for that card: `1g.10gb`, `1g.20gb`, `2g.20gb`, `3g.40gb`, `4g.40gb`, `7g.80gb`.

Six devices from one card. The overlap is the point — and there is deliberately
no spare GPU, because with one available a claim that "should" contend would
simply be satisfied elsewhere and the scenario would pass while demonstrating
nothing.

## Run it

```sh
kubectl apply -f examples/mig-exclusivity/
```

```console
$ ghostgpu status
POOL      MODE  NODES  DEVICES  FAULTED  OCCUPIED  ALLOCATED  FREE
mig-demo  mig   1      6        0        0         0          6
```

Six devices, but the card cannot satisfy six claims. Take the whole card:

```sh
kubectl apply -f examples/mig-exclusivity/jobs/whole-card.yaml
```

Then try to take a slice of it:

```sh
kubectl apply -f examples/mig-exclusivity/jobs/one-slice.yaml
```

The second stays **Pending** — the `7g.80gb` consumed every compute slice and
all the memory, so no other profile on that silicon can be realised:

```console
$ ghostgpu status --budgets
NODE         GPU    SLICES  MEMORY
ghost-migdemo-0  gpu-0  7/7     80Gi/80Gi
```

Delete the whole-card job and the slice schedules, which is the check that the
Pending was caused by exclusivity rather than by anything else.

## How this is enforced

By the upstream scheduler, through DRA shared counters. ghostgpu publishes each
card's compute-slice and memory budget as a counter set, and each MIG instance
declares what it consumes. **ghostgpu contributes no allocation logic** — which
is what makes this a test of your scheduler rather than a test of ghostgpu.

## What to point at it

- **A scheduler or scheduling plugin** that claims MIG awareness.
- **A queueing system** deciding whether a job's profile request is satisfiable.
- **Capacity planning tooling** — twelve devices, but not twelve jobs.
