//go:build e2e
// +build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/santimillang/ghostgpu/test/utils"
)

const (
	simNodeA    = "ghost-e2e-0"
	simNodeB    = "ghost-e2e-1"
	unmanaged   = "ghost-e2e-real"
	e2ePoolName = "h100-pool"
)

func testdata(name string) string {
	return filepath.Join("test", "e2e", "testdata", name)
}

// kubectlOut runs kubectl and returns trimmed stdout, failing the spec on error.
func kubectlOut(args ...string) string {
	out, err := utils.Run(exec.Command("kubectl", args...))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// kubectlTry runs kubectl and returns output and error without failing.
func kubectlTry(args ...string) (string, error) {
	out, err := utils.Run(exec.Command("kubectl", args...))
	return strings.TrimSpace(out), err
}

func jsonPath(g Gomega, kind, name, path string) string {
	out, err := utils.Run(exec.Command("kubectl", "get", kind, name, "-o", "jsonpath="+path))
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

func podPhase(g Gomega, name string) string {
	return jsonPath(g, "pod", name, "{.status.phase}")
}

// This is the only place in the project where scheduling *decisions* can be
// asserted. envtest runs no kube-scheduler, so every claim about placement,
// capacity accounting, or DRA allocation has to be made here, against a real
// control plane driving nodes that do not exist.
var _ = Describe("Simulated GPUs", Ordered, func() {
	BeforeAll(func() {
		By("creating the simulated nodes")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("nodes.yaml")))
		Expect(err).NotTo(HaveOccurred(), "Failed to create simulated nodes")

		By("waiting for the controller-manager to be available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the GPUModel and GPUPool")
		_, err = utils.Run(exec.Command("kubectl", "apply", "-f", testdata("pool.yaml")))
		Expect(err).NotTo(HaveOccurred(), "Failed to create the pool")
	})

	AfterAll(func() {
		for _, f := range []string{"dra.yaml", "pods-extended-resource.yaml", "pool.yaml", "nodes.yaml"} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", testdata(f), "--ignore-not-found"))
		}
	})

	It("advertises extended-resource capacity on simulated nodes", func() {
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "node", simNodeA, `{.status.capacity.nvidia\.com/gpu}`)).To(Equal("8"))
			g.Expect(jsonPath(g, "node", simNodeA, `{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("8"))
			g.Expect(jsonPath(g, "node", simNodeB, `{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("8"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	// Real tooling selects on these exact keys. If they drift, ghostgpu's nodes
	// become invisible to the software it exists to test.
	It("applies GPU Feature Discovery labels", func() {
		Eventually(func(g Gomega) {
			labels := map[string]string{
				`{.metadata.labels.nvidia\.com/gpu\.present}`:        "true",
				`{.metadata.labels.nvidia\.com/gpu\.count}`:          "8",
				`{.metadata.labels.nvidia\.com/gpu\.product}`:        "NVIDIA-H100-SXM",
				`{.metadata.labels.nvidia\.com/gpu\.memory}`:         "81920",
				`{.metadata.labels.nvidia\.com/gpu\.compute\.major}`: "9",
				`{.metadata.labels.nvidia\.com/gpu\.compute\.minor}`: "0",
			}
			for path, want := range labels {
				g.Expect(jsonPath(g, "node", simNodeA, path)).To(Equal(want), "label path %s", path)
			}
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	It("publishes one DRA ResourceSlice per simulated node", func() {
		Eventually(func(g Gomega) {
			for _, node := range []string{simNodeA, simNodeB} {
				name := e2ePoolName + "-" + node
				devices := jsonPath(g, "resourceslice", name, "{.spec.devices[*].name}")
				g.Expect(strings.Fields(devices)).To(HaveLen(8), "slice %s", name)
				g.Expect(jsonPath(g, "resourceslice", name, "{.spec.driver}")).
					To(Equal("gpu.ghostgpu.dev"))
			}
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	It("reports pool status", func() {
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", e2ePoolName, "{.status.nodesMatched}")).To(Equal("2"))
			g.Expect(jsonPath(g, "gpupool", e2ePoolName, "{.status.devicesPublished}")).To(Equal("16"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	// The safety invariant from design spec §5. ghost-e2e-real matches the
	// pool's nodeSelector but carries no kwok annotation, so it stands in for a
	// production GPU node in a cluster where ghostgpu was installed by mistake.
	// Nothing about it may change, ever.
	It("never modifies a node that is not kwok-managed", func() {
		// Give the controller time to have done the wrong thing if it were going to.
		Consistently(func(g Gomega) {
			g.Expect(jsonPath(g, "node", unmanaged, `{.status.capacity.nvidia\.com/gpu}`)).
				To(BeEmpty(), "SAFETY VIOLATION: capacity was patched onto an unmanaged node")
			g.Expect(jsonPath(g, "node", unmanaged, `{.metadata.labels.nvidia\.com/gpu\.present}`)).
				To(BeEmpty(), "SAFETY VIOLATION: GFD labels were applied to an unmanaged node")
		}, 20*time.Second, 4*time.Second).Should(Succeed())

		_, err := kubectlTry("get", "resourceslice", e2ePoolName+"-"+unmanaged)
		Expect(err).To(HaveOccurred(),
			"SAFETY VIOLATION: a ResourceSlice was published for an unmanaged node")
	})

	Context("the extended-resource path", func() {
		It("schedules pods against simulated capacity and refuses to overcommit it", func() {
			By("submitting two pods that together consume all 8 GPUs, plus one more")
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f",
				testdata("pods-extended-resource.yaml")))
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the two fitting pods to run")
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "e2e-gpu-a")).To(Equal("Running"))
				g.Expect(podPhase(g, "e2e-gpu-b")).To(Equal("Running"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("asserting the overflow pod stays Pending")
			Consistently(func(g Gomega) {
				g.Expect(podPhase(g, "e2e-gpu-overflow")).To(Equal("Pending"),
					"scheduler is not accounting for simulated GPU capacity")
			}, 20*time.Second, 4*time.Second).Should(Succeed())

			By("checking the scheduler said why")
			events := kubectlOut("get", "events", "--field-selector",
				"involvedObject.name=e2e-gpu-overflow", "-o", "jsonpath={.items[*].message}")
			Expect(events).To(ContainSubstring("Insufficient nvidia.com/gpu"))
		})
	})

	Context("the DRA path", func() {
		// The load-bearing result: the scheduler records exact device identity
		// in ResourceClaim.status, which is why ghostgpu needs no pod-to-GPU
		// binding store and works with no kubelet at all.
		It("allocates specific devices honouring a topology constraint", func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("dra.yaml")))
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the pod to run")
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "e2e-dra-job")).To(Equal("Running"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("reading which devices the scheduler chose")
			var devices []string
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "resourceclaims",
					"-o", "jsonpath={.items[*].status.allocation.devices.results[*].device}"))
				g.Expect(err).NotTo(HaveOccurred())
				devices = strings.Fields(strings.TrimSpace(out))
				g.Expect(devices).To(HaveLen(2), "expected exactly two allocated devices")
			}, time.Minute, 2*time.Second).Should(Succeed())

			// nvlinkDomainSize is 4, so domain-1 is exactly gpu-4..gpu-7.
			// Anything outside that range means the CEL selector was ignored.
			for _, d := range devices {
				Expect(d).To(BeElementOf("gpu-4", "gpu-5", "gpu-6", "gpu-7"),
					"device %s is not in NVLink domain-1; the topology selector was not honoured", d)
			}
		})
	})
})
