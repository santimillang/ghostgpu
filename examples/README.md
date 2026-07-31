# Scenarios

Each directory here is a question you might need to answer about your own
tooling, and a fleet that lets you answer it.

| Scenario | The question it answers |
| --- | --- |
| [`fragmented-fleet`](fragmented-fleet) | Seven GPUs are free but spread 2/2/2/1. Does my four-GPU job schedule? Should it? |
| [`idle-reclamation`](idle-reclamation) | One job is using its GPU, another is squatting on one. Does my reclaimer pick the right one? |
| [`gpu-failure`](gpu-failure) | A GPU fails under a running job. Does my remediation drain the node and requeue the work? |
| [`mig-exclusivity`](mig-exclusivity) | Six MIG profiles are offered by one card. Does my scheduler understand it can satisfy only some of them at once? |

Every scenario is applied and checked by CI, so if one of these stops working
the build fails. An example that has quietly rotted is worse than no example.

## Running one

All of them assume the [quickstart](../README.md#quickstart) cluster and a
running operator. Then:

```sh
kubectl apply -f examples/<scenario>/
```

Each README says what to expect and which command shows it.

## Two things worth knowing first

**Simulated nodes carry a taint.** kwok's convention keeps real workloads off
fake hardware, so pods in these scenarios tolerate
`kwok.x-k8s.io/node=fake:NoSchedule`. Yours will need to as well.

**`ghostgpu status` answers most questions** without hand-writing jsonpath
against `ResourceClaim`s:

```sh
ghostgpu status                  # what exists, and how much of it is free
ghostgpu status --devices        # which pod holds which device
ghostgpu status --budgets        # why another MIG instance will not fit
```
