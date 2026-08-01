# `ghostgpu status`

Scheduling is the point, so ghostgpu can tell you what the scheduler did without hand-writing jsonpath against `ResourceClaim`s.

```console
$ ghostgpu status
POOL       MODE  NODES  DEVICES  OCCUPIED  ALLOCATED  FREE
h100-pool  mig   2      40       10        1          29
```

## Per node

```console
$ ghostgpu status --node ghost-mig-0
NODE         DEVICE           PROFILE  STATUS     POD
ghost-mig-0  gpu-0-1g-10gb-0  1g.10gb  free       -
ghost-mig-0  gpu-0-3g-40gb-0  3g.40gb  allocated  default/trainer
ghost-mig-0  gpu-1-1g-10gb-0  1g.10gb  occupied   (declared)
```

Occupied, allocated, and faulted are reported as three distinct states with different owners. A device declared busy reports `(declared)` rather than naming a pod that does not exist.

## Per-GPU budgets

```console
$ ghostgpu status --budgets --node ghost-mig-0
NODE         GPU    SLICES  MEMORY
ghost-mig-0  gpu-0  3/7     40Gi/80Gi
ghost-mig-0  gpu-1  0/7     0/80Gi
```

The budget view answers the question that is genuinely tedious to work out by hand: how much of a physical GPU is already spoken for, and therefore why another MIG instance will not fit.

Everything is derived from objects already in the cluster — ghostgpu stores no allocation state of its own.
