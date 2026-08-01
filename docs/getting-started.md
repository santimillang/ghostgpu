# Getting started

You need [`kwokctl`](https://kwok.sigs.k8s.io/docs/user/installation/), `kind`, and Docker.

## 1. A kwok cluster with DRA enabled

```sh
kwokctl create cluster --name ghostgpu --runtime kind \
  --kube-feature-gates "DynamicResourceAllocation=true" \
  --kube-runtime-config "resource.k8s.io/v1=true"
```

A fresh cluster has no simulated nodes, so there is nothing yet to put GPUs on. Add some:

```sh
kwokctl scale node --name ghostgpu --replicas 2
```

!!! note "The operator runs on the real node"
    `kwokctl` cordons the single real node so simulated workload stays off the machine hosting it. ghostgpu's operator is *not* simulated — it is an ordinary Deployment that needs a real node — so it ships with a toleration for `node.kubernetes.io/unschedulable`. Without that it would sit `Pending` forever in exactly the cluster this guide tells you to create.

## 2. Install ghostgpu

=== "Helm"

    ```sh
    helm install ghostgpu oci://ghcr.io/santimillang/charts/ghostgpu \
      --namespace ghostgpu-system --create-namespace
    ```

=== "Single manifest"

    ```sh
    kubectl apply -f https://github.com/santimillang/ghostgpu/releases/latest/download/install.yaml
    ```

Installing is safe alongside real hardware: ghostgpu only ever modifies nodes carrying kwok's `kwok.x-k8s.io/node` annotation.

!!! note "Chart versions drop the leading `v`"
    The git tag `v0.1.0` publishes chart version `0.1.0`. `helm install --version v0.1.0` fails with `FetchReference ... not found`.

## 3. Get the CLI

```sh
curl -sSfL https://github.com/santimillang/ghostgpu/releases/latest/download/ghostgpu_linux_amd64.tar.gz \
  | tar xz ghostgpu
```

Archives are published for `linux`/`darwin` on `amd64`/`arm64`, and `windows_amd64.zip`. The filenames never carry a version, so these URLs stay valid across releases.

## 4. Give your kwok nodes some GPUs

```sh
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
./ghostgpu up --gpu NVIDIA-A100-SXM4-40GB --memory 40Gi \
  --compute-capability 8.0 --dry-run | kubectl apply -f -
```

## Declaring a fleet in Helm values

If you install with Helm, the fleet can be declared alongside the operator:

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

!!! warning "`productName` becomes a node label value"
    It must be a valid Kubernetes label value — no spaces. Use `NVIDIA-H100-80GB-HBM3`, not `NVIDIA H100 80GB HBM3`.

## Checking it worked

`helm install --wait` waits for the operator's Deployment, not for your fleet. It exits 0 while a `GPUPool` is still failing to reconcile, so a broken fleet can look like a successful install. Check the pool itself:

```sh
kubectl get gpupool -o wide
```

`DEVICES` reaching the count you declared is the signal that the fleet exists.

## Where next

- [Worked scenarios](scenarios.md) — each one a question about your own tooling
- [MIG](simulating/mig.md), [faults](simulating/faults.md), [occupancy](simulating/occupancy.md), [metrics](simulating/metrics.md)
- [`ghostgpu capture`](tools/capture.md) — reproduce a real cluster's fleet
