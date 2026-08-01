# Injecting hardware failures

Hardware failure is the hardest thing to test for, because you cannot arrange it on demand — there is no way to ask a production GPU to fall off the bus so you can see whether your remediation drains the node.

Declare it instead:

```yaml
spec:
  faults:
    - nodeSelector: {rack: a}
      gpus: 1
      effect: Evict        # the card is gone; the job must move
      xid: 79              # reported on DCGM_FI_DEV_XID_ERRORS
```

## Effects

`Evict` models device loss and uncorrectable ECC. The workload running on that GPU is **thrown off and its `ResourceClaim` released**, so it can be rescheduled onto healthy hardware — which is the behaviour a remediation or requeueing system under test actually needs to exercise.

`Unschedulable` models a card that still runs but must take no new work, such as one with a row remap pending a reboot.

## The XID

The `xid` surfaces on `DCGM_FI_DEV_XID_ERRORS`, which is the signal most remediation watches. It also rides along on the device taint value, so `kubectl get resourceslice` explains why a device is out of service without anyone scraping metrics.

## Composition with occupancy

Faults and occupancy are independent declarations applied lowest-index-first, and a fault wins where they overlap — so "three busy, one faulted" means the failure happened to a GPU that was working.

Repairing a fleet is just removing the entry, which makes a pending job schedulable again.

## Verified, not assumed

The mechanism was chosen from a spike rather than from documentation. A `NoExecute` DRA device taint was verified live to evict a running workload **and clear its claim allocation** — that deallocation is what lets the job reschedule — with a negative control confirming an identical untainted pod survives the same wait.

See the [fault injection spike findings](../design/2026-07-31-fault-injection-spike-findings.md).
