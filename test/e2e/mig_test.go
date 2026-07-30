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
	migNode     = "ghost-mig-0"
	migPoolName = "mig-pool"

	// The GPU both claims target. Its counter set lives in the *second*
	// counter slice, since the first holds sets for gpu-0 through gpu-7.
	// Choosing gpu-9 is what makes this suite exercise cross-slice counter
	// resolution rather than merely re-testing a single-slice pool.
	migTargetGPU = 9
)

// applyYAML pipes a manifest into kubectl, for objects that can only be built
// once runtime values are known.
func applyYAML(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

// migClaim renders a ResourceClaim and Pod pinned to one profile on one
// physical GPU.
//
// Selecting by uuid as well as profile is essential: with 16 GPUs available,
// a claim selecting only by profile would be satisfied on a different card and
// the exclusivity assertion would pass without ever testing exclusivity.
func migClaim(name, profile, uuid string) string {
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
          deviceClassName: ghostgpu-mig-e2e
          count: 1
          allocationMode: ExactCount
          selectors:
            - cel:
                expression: >-
                  device.attributes["gpu.ghostgpu.dev"].migProfile == %[2]q &&
                  device.attributes["gpu.ghostgpu.dev"].uuid == %[3]q
---
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
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
`, name, profile, uuid)
}

func claimDevice(g Gomega, claim string) string {
	out, err := utils.Run(exec.Command("kubectl", "get", "resourceclaim", claim,
		"-o", "jsonpath={.status.allocation.devices.results[0].device}"))
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// MIG is ghostgpu's clearest differentiator, and the only part of it that
// cannot be checked without a scheduler is the part that matters: overlapping
// profiles on one physical GPU must be mutually exclusive, enforced upstream
// with no allocation logic of ghostgpu's own.
var _ = Describe("MIG", Ordered, func() {
	var targetUUID string

	BeforeAll(func() {
		By("creating a 16-GPU simulated node")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("mig-nodes.yaml")))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the controller-manager to be available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the MIG pool")
		_, err = utils.Run(exec.Command("kubectl", "apply", "-f", testdata("mig-pool.yaml")))
		Expect(err).NotTo(HaveOccurred())

		_, err = utils.Run(exec.Command("kubectl", "apply", "-f", testdata("mig-deviceclass.yaml")))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		for _, pod := range []string{"e2e-mig-whole", "e2e-mig-slice"} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", pod, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "resourceclaim", pod+"-claim", "--ignore-not-found"))
		}
		for _, f := range []string{"mig-deviceclass.yaml", "mig-pool.yaml", "mig-nodes.yaml"} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", testdata(f), "--ignore-not-found"))
		}
	})

	// 16 GPUs needs two counter slices, because at most 8 counter sets fit in
	// one. 16 x 6 profiles = 96 devices needs two device slices, at 64 each.
	It("shards slices across the measured API limits", func() {
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "resourceslices",
				"-l", "ghostgpu.dev/pool="+migPoolName,
				"-o", "jsonpath={.items[*].metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.Fields(out)).To(HaveLen(4),
				"expected 2 counter slices + 2 device slices for a 16-GPU MIG pool")
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("checking no slice exceeds the per-slice limits")
		counters := kubectlOut("get", "resourceslices", "-l", "ghostgpu.dev/pool="+migPoolName,
			"-o", "jsonpath={range .items[*]}{.spec.sharedCounters[*].name}{\"\\n\"}{end}")
		total := 0
		for _, line := range strings.Split(strings.TrimSpace(counters), "\n") {
			names := strings.Fields(line)
			Expect(len(names)).To(BeNumerically("<=", 8),
				"a slice declared more than 8 counter sets")
			total += len(names)
		}
		Expect(total).To(Equal(16), "expected one counter set per physical GPU")
	})

	It("advertises mixed-strategy extended resources", func() {
		Eventually(func(g Gomega) {
			// 7 instances of 1g.10gb per GPU across 16 GPUs.
			g.Expect(jsonPath(g, "node", migNode, `{.status.capacity.nvidia\.com/mig-1g\.10gb}`)).
				To(Equal("112"))
			// Only one 7g.80gb fits per card.
			g.Expect(jsonPath(g, "node", migNode, `{.status.capacity.nvidia\.com/mig-7g\.80gb}`)).
				To(Equal("16"))
			// No whole card is separately allocatable once partitioned.
			g.Expect(jsonPath(g, "node", migNode, `{.status.capacity.nvidia\.com/gpu}`)).
				To(Equal("0"))
			g.Expect(jsonPath(g, "node", migNode, `{.metadata.labels.nvidia\.com/mig\.capable}`)).
				To(Equal("true"))
			g.Expect(jsonPath(g, "node", migNode, `{.metadata.labels.nvidia\.com/mig\.strategy}`)).
				To(Equal("mixed"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	It("reports MIG status on the pool", func() {
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", migPoolName, "{.status.devicesPublished}")).To(Equal("96"))
			g.Expect(jsonPath(g, "gpupool", migPoolName, "{.status.migProfilesPublished}")).To(Equal("6"))
			g.Expect(jsonPath(g, "gpupool", migPoolName, "{.status.nodesMatched}")).To(Equal("1"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	Context("exclusivity across a counter-slice boundary", func() {
		It("resolves the target GPU's identity", func() {
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "resourceslices",
					"-l", "ghostgpu.dev/pool="+migPoolName,
					"-o", fmt.Sprintf(
						`jsonpath={range .items[*].spec.devices[?(@.name=="gpu-%d-7g-80gb")]}`+
							`{.attributes.uuid.string}{end}`, migTargetGPU)))
				g.Expect(err).NotTo(HaveOccurred())
				targetUUID = strings.TrimSpace(out)
				g.Expect(targetUUID).NotTo(BeEmpty(),
					"no uuid for gpu-%d; its devices were never published", migTargetGPU)
			}, 2*time.Minute, 2*time.Second).Should(Succeed())
		})

		It("allocates a whole-GPU profile", func() {
			Expect(applyYAML(migClaim("e2e-mig-whole", "7g.80gb", targetUUID))).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "e2e-mig-whole")).To(Equal("Running"))
				g.Expect(claimDevice(g, "e2e-mig-whole-claim")).
					To(Equal(fmt.Sprintf("gpu-%d-7g-80gb", migTargetGPU)))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())
		})

		// The load-bearing assertion. 7g.80gb consumes the GPU's entire budget,
		// so no other profile on that card can be satisfied — and the counter
		// set proving it lives in a different slice from the devices.
		It("blocks an overlapping profile on the same physical GPU", func() {
			Expect(applyYAML(migClaim("e2e-mig-slice", "1g.10gb", targetUUID))).To(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(podPhase(g, "e2e-mig-slice")).To(Equal("Pending"),
					"MIG profiles on one GPU are not mutually exclusive")
			}, 25*time.Second, 5*time.Second).Should(Succeed())
		})

		// Without this, "Pending" proves nothing: it could be any unrelated
		// scheduling constraint. Releasing the blocking claim and watching the
		// blocked one schedule is what identifies the counters as the cause.
		It("unblocks it once the whole-GPU claim is released", func() {
			_, err := utils.Run(exec.Command("kubectl", "delete", "pod", "e2e-mig-whole", "--wait=true"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "delete", "resourceclaim",
				"e2e-mig-whole-claim", "--wait=true"))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "e2e-mig-slice")).To(Equal("Running"))
				g.Expect(claimDevice(g, "e2e-mig-slice-claim")).
					To(Equal(fmt.Sprintf("gpu-%d-1g-10gb", migTargetGPU)))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())
		})
	})
})
