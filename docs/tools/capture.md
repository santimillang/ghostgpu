# `ghostgpu capture`

Describing your fleet by hand is guesswork about exactly the details that matter — the ones that differ from the defaults are usually the ones causing the bug you are chasing.

`ghostgpu capture` reads a cluster that already has GPUs and prints the manifests that reproduce it:

```sh
ghostgpu capture --context prod-us-east > fleet.yaml
ghostgpu capture --context prod-us-east | kubectl apply -f -
```

## What it reads

Everything comes from what such a cluster already publishes:

- **GFD labels** give the product, memory, and compute capability
- **`nvidia.com/mig-*` capacity** gives the per-GPU MIG layout
- **`ResourceSlice` attributes** give NVLink domains and NUMA locality, which no node label carries

Distinct node shapes become distinct pools, and the kwok `Node` manifests come with them, so applying the output is enough to have the fleet. `--nodes=false` prints only the pools.

A node's applied `status.capacity` survives both `kubectl apply` and kwok's reconciliation, so captured nodes really do reproduce the source machine's CPU and memory.

## It only ever reads

The client is narrowed to a read-only type before the command is handed one, so there is no create, update, or delete for it to call. Pointing a simulator at production has to be **provably** harmless, not merely intended to be — the e2e suite checks it against a live API server by asserting resourceVersions are unchanged.

The manifests go to stdout, so applying them stays your own explicit act, and node names are synthesised rather than copied so captured output is safe to paste into an issue.

## It is lossy by design, and says so

Capture reproduces *shape*, not workloads. Anything it cannot reproduce faithfully is reported on stderr, so `> fleet.yaml` still yields a clean file:

- a non-uniform MIG layout
- a node whose GFD labels are incomplete
- the `single` MIG strategy
- MIG on a node whose physical GPU count nothing in the cluster reveals

MIG profiles for hardware ghostgpu has no table for are derived from the profile *name* — `3g.40gb` means 3 slices and 40Gi — a reading asserted against every built-in table rather than assumed.
