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
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/santimillang/ghostgpu/test/utils"
)

const (
	occupancyPool = "occupancy-pool"
	fragRackA0    = "ghost-frag-a0"
	fragRackC0    = "ghost-frag-c0"
)

// gpuClaim renders a ResourceClaim for count whole GPUs, plus the pod holding
// it. No device selector: the point is whether the cluster has room anywhere,
// not whether a particular card can be picked.
func gpuClaim(name string, count int, deviceClass string) string {
	return fmt.Sprintf(`
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: %[1]s-claim
spec:
  devices:
    requests:
      - name: gpu
        exactly:
          deviceClassName: %[3]s
          count: %[2]d
          allocationMode: ExactCount
---
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  labels:
    ghostgpu.dev/e2e-workload: %[1]s
spec:
  tolerations:
    - key: kwok.x-k8s.io/node
      operator: Equal
      value: fake
      effect: NoSchedule
  containers:
    - name: trainer
      image: fake-trainer:latest
      resources:
        claims:
          - name: gpu
  resourceClaims:
    - name: gpu
      resourceClaimName: %[1]s-claim
`, name, count, deviceClass)
}

// occupancyClass is the DeviceClass the fragmentation fixtures publish.
const occupancyClass = "ghostgpu-occupancy-e2e"

// Fragmentation is the scenario ghostgpu could not express until now: every
// simulated GPU started free, so the only bugs testable were about capacity.
//
// This fleet has seven GPUs free out of sixteen, but spread so that no node has
// more than two. Whether a four-GPU job schedules against it is a real question
// with a real answer, and only a real scheduler can give it.
var _ = Describe("occupancy", Ordered, func() {
	BeforeAll(func() {
		By("waiting for the controller-manager to be available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("creating a fragmented fleet")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("occupancy.yaml")))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", occupancyPool, "{.status.nodesMatched}")).To(Equal("4"))
			g.Expect(jsonPath(g, "gpupool", occupancyPool, "{.status.devicesPublished}")).To(Equal("16"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		for _, pod := range []string{"frag-small", "frag-large", "frag-large-retry"} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", pod, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "resourceclaim", pod+"-claim", "--ignore-not-found"))
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
			testdata("occupancy.yaml"), "--ignore-not-found"))
	})

	// Occupied devices stay published. A fleet that is 60% full has the same
	// hardware as an empty one, and shrinking it would model a smaller cluster
	// rather than a busier one.
	It("keeps occupied devices published and reports how many", func() {
		Eventually(func(g Gomega) {
			// rack a and b are 2 busy of 4, rack c is 3: 2+2+2+3 = 9.
			g.Expect(jsonPath(g, "gpupool", occupancyPool, "{.status.gpusOccupied}")).To(Equal("9"))
		}, time.Minute, 2*time.Second).Should(Succeed())

		devices := kubectlOut("get", "resourceslices",
			"-l", "ghostgpu.dev/pool="+occupancyPool,
			"-o", "jsonpath={range .items[*].spec.devices[*]}{.name}{\"\\n\"}{end}")
		Expect(strings.Fields(devices)).To(HaveLen(16),
			"occupancy must not delete devices, only make them unavailable")
	})

	It("taints exactly the occupied devices, lowest index first", func() {
		tainted := func(node string) []string {
			out := kubectlOut("get", "resourceslices",
				"-l", "ghostgpu.dev/pool="+occupancyPool,
				"-o", fmt.Sprintf(
					`jsonpath={range .items[?(@.spec.nodeName=="%s")].spec.devices[*]}`+
						`{.name}{"="}{.taints[0].key}{"\n"}{end}`, node))

			var busy []string
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				name, key, _ := strings.Cut(line, "=")
				if key != "" {
					busy = append(busy, name)
				}
			}
			return busy
		}

		// Lowest index first, so the same declaration always produces the same
		// fragmentation rather than one that depends on iteration order.
		Expect(tainted(fragRackA0)).To(Equal([]string{"gpu-0", "gpu-1"}))
		Expect(tainted(fragRackC0)).To(Equal([]string{"gpu-0", "gpu-1", "gpu-2"}),
			"rack c is declared 3 busy by the first matching entry")
	})

	// The legacy path expresses the same fleet as allocatable below capacity,
	// which is what that distinction already means in Kubernetes.
	It("advertises the occupancy on the extended-resource path too", func() {
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "node", fragRackA0, `{.status.capacity.nvidia\.com/gpu}`)).To(Equal("4"))
			g.Expect(jsonPath(g, "node", fragRackA0, `{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("2"))
			g.Expect(jsonPath(g, "node", fragRackC0, `{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("1"))
		}, time.Minute, 2*time.Second).Should(Succeed())
	})

	// The assertion the whole feature exists for.
	It("refuses a job that fits the cluster but no single node", func() {
		By("placing a 2-GPU job, which any node still has room for")
		Expect(applyYAML(gpuClaim("frag-small", 2, occupancyClass))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(podPhase(g, "frag-small")).To(Equal("Running"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("submitting a 4-GPU job while 7 GPUs are free but fragmented")
		Expect(applyYAML(gpuClaim("frag-large", 4, occupancyClass))).To(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(podPhase(g, "frag-large")).To(Equal("Pending"))
		}, 30*time.Second, 5*time.Second).Should(Succeed(),
			"a 4-GPU job was placed even though no node has 4 free GPUs")
	})

	// The negative control. Without it, the Pending above could be caused by
	// anything — a broken DeviceClass, a claim the scheduler never saw. Lifting
	// the occupancy has to make the very same job schedulable.
	It("places that job once the occupancy is lifted", func() {
		By("emptying rack a")
		_, err := utils.Run(exec.Command("kubectl", "patch", "gpupool", occupancyPool,
			"--type=json", "-p", `[{"op":"replace","path":"/spec/occupancy","value":[`+
				`{"nodeSelector":{"rack":"c"},"busyPerNode":3},`+
				`{"nodeSelector":{"rack":"b"},"busyPerNode":2},`+
				`{"busyPerNode":0}]}]`))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "node", fragRackA0, `{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("4"))
		}, time.Minute, 2*time.Second).Should(Succeed())

		// The same pod that could not be placed a moment ago.
		Eventually(func(g Gomega) {
			g.Expect(podPhase(g, "frag-large")).To(Equal("Running"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed(),
			"lifting the occupancy did not make the pending job schedulable, "+
				"so its Pending was caused by something other than the taints")
	})
})
