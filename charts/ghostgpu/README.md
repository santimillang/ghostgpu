# ghostgpu

A Helm chart for [ghostgpu](https://github.com/santimillang/ghostgpu): simulate GPU clusters on Kubernetes so GPU-aware schedulers, autoscalers, and platform tooling can be tested with zero GPU hardware.

This chart installs the operator (CRDs, RBAC, and the controller Deployment) and, optionally, a fleet of `GPUModel`/`GPUPool` resources declared directly in `values.yaml`.

## Install

### OCI (recommended)

```sh
helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --namespace ghostgpu-system --create-namespace
```

Pin a specific chart version with `--version`. Chart versions drop the git
tag's leading `v` — the tag `v0.1.0` is chart version `0.1.0`:

```sh
helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --version 0.1.0 \
  --namespace ghostgpu-system --create-namespace
```

`helm install --version v0.1.0` (with the `v`) fails with
`FetchReference ... not found` — the OCI registry only knows the stripped
form.

### `helm repo add`

A classic Helm repository is published alongside the OCI one:

```sh
helm repo add ghostgpu https://santimillang.github.io/ghostgpu
helm repo update
helm install ghostgpu ghostgpu/ghostgpu \
  --namespace ghostgpu-system --create-namespace
```

## Values

| Key | Type | Default | Meaning |
|---|---|---|---|
| `image.repository` | string | `ghcr.io/santimillang/ghostgpu` | the operator image |
| `image.tag` | string | `""` (falls back to the chart's `appVersion`) | override to pin an operator version independent of the chart |
| `image.pullPolicy` | string | `IfNotPresent` | standard Kubernetes pull policy |
| `gpuModels` | list | `[]` | rendered verbatim into `GPUModel` resources — see below |
| `gpuPools` | list | `[]` | rendered verbatim into `GPUPool` resources — see below |

`gpuModels` and `gpuPools` are passed through untouched: each entry's `spec`
becomes the CR's `spec` with no chart-side validation, because the CRD's
OpenAPI schema is already the validator and a copy of it here would be a
second schema to keep in sync with the first.

`spec.productName` becomes the value of the `nvidia.com/gpu.product` node
label, not just descriptive text, so it must be a valid Kubernetes label
value: no spaces, only alphanumerics, `-`, `_`, and `.`. Use
`NVIDIA-H100-80GB-HBM3`, not `NVIDIA H100 80GB HBM3` — the latter installs
without error and then leaves the controller looping on an invalid label,
with `devicesPublished` never populating (see "Checking the install worked"
below).

### Worked example

[`values-example.yaml`](values-example.yaml) declares a two-node H100 fleet
that starts two-thirds full, so a fragmentation scenario exists before any
workload is submitted:

```yaml
gpuModels:
  - name: h100
    spec:
      productName: NVIDIA-H100-80GB-HBM3
      memory: 80Gi
      computeCapability: "9.0"

gpuPools:
  - name: h100-pool
    spec:
      modelRef: h100
      gpusPerNode: 8
      occupancy:
        - busyPerNode: 2
```

```sh
helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --namespace ghostgpu-system --create-namespace \
  -f values-example.yaml
```

## Upgrading

Helm does not update CRDs in a chart's `crds/` directory on `helm upgrade`.
When a release changes the ghostgpu API, apply the new CRDs by hand before
upgrading:

```sh
helm show crds oci://ghcr.io/santimillang/charts/ghostgpu --version 0.2.0 \
  | kubectl apply -f -
```

`helm show crds` prints only the contents of the chart's `crds/` directory —
nothing else in the release — so this is the one command that both selects
the right resources and works with no CRD labels to select on (the CRDs in
this chart carry none). Then upgrade normally:

```sh
helm upgrade ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
  --version 0.2.0 --namespace ghostgpu-system
```

They live in `crds/` rather than `templates/` because Helm resolves the whole
release's resource kinds before creating anything, so a `GPUPool` declared in
values alongside its own CRD would fail on first install.

## Checking the install worked

`helm install --wait` waits for the operator's Deployment, not for your
fleet. It exits 0 while a `GPUPool` is still failing to reconcile, so a
broken fleet looks like a successful install. Check the pool itself:

```sh
kubectl get gpupool -o wide
```

`DEVICES` reaching the count you declared is the signal that the fleet
exists.

This is not hypothetical: a `productName` containing spaces produced exactly
that outcome — `helm install` reported success while the controller looped
on an invalid node label and `devicesPublished` never populated.

## Uninstalling

```sh
helm uninstall ghostgpu --namespace ghostgpu-system
```

Helm does not remove CRDs it did not create the templates for — the ones in
`crds/` are left in place, along with any `GPUModel`/`GPUPool` objects still
using them. Delete them explicitly if you want the API types gone too:

```sh
kubectl delete crd gpumodels.ghostgpu.dev gpupools.ghostgpu.dev
```
