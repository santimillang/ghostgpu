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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/santimillang/ghostgpu/test/utils"
)

const (
	// The pool being captured, and the nodes it manages.
	sourcePool    = "capture-source-pool"
	sourceNodeA   = "ghost-capture-0"
	sourceNodeB   = "ghost-capture-1"
	sourceDevices = "40" // 2 nodes x 4 GPUs x 5 declared instances

	// What capture derives from that cluster. The names come from the product
	// string alone, which is the only thing capture has to name a pool after.
	capturedPool  = "h100-80gb-hbm3-pool"
	capturedModel = "h100-80gb-hbm3"
)

// ghostgpuCapture runs the CLI, keeping stdout and stderr apart.
//
// utils.Run merges the two, which would hide the property this suite has to
// check: warnings must not land in the manifest stream, or
// `ghostgpu capture > fleet.yaml` produces a file kubectl cannot read.
func ghostgpuCapture(args ...string) (stdout, stderr string) {
	dir, err := utils.GetProjectDir()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	cmd := exec.Command("./bin/ghostgpu", append([]string{"capture"}, args...)...)
	cmd.Dir = dir
	cmd.Env = os.Environ()

	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	ExpectWithOffset(1, cmd.Run()).To(Succeed(), errOut.String())
	return out.String(), errOut.String()
}

// The claim `ghostgpu capture` makes is that it can reproduce a cluster it was
// never told about, reading only what that cluster already publishes. Unit
// tests can assert the derivation; only a real API server can show that the
// manifests it prints are accepted, that kwok adopts the nodes in them, and
// that the operator republishes the same fleet from the other side.
//
// The source pool is statically partitioned on purpose. It is the shape where
// every derivation is load-bearing at once: the physical GPU count cannot come
// from gpu.count, the partition has to come from extended resources, and the
// NVLink domain size exists only on device attributes.
var _ = Describe("capture", Ordered, func() {
	var captured string

	BeforeAll(func() {
		By("waiting for the controller-manager to be available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("standing up the cluster to be captured")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("capture-source.yaml")))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for it to publish its fleet")
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", sourcePool, "{.status.devicesPublished}")).To(Equal(sourceDevices))
			g.Expect(jsonPath(g, "gpupool", sourcePool, "{.status.nodesMatched}")).To(Equal("2"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "gpupool", capturedPool, "--ignore-not-found"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "gpumodel", capturedModel, "--ignore-not-found"))
		for i := range 2 {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "node",
				fmt.Sprintf("%s-%d", capturedModel, i), "--ignore-not-found"))
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
			testdata("capture-source.yaml"), "--ignore-not-found"))
	})

	It("derives the fleet from what the cluster publishes", func() {
		var warnings string
		captured, warnings = ghostgpuCapture()

		Expect(warnings).To(BeEmpty(),
			"capturing a cluster ghostgpu itself published should be lossless")

		Expect(captured).To(ContainSubstring("productName: NVIDIA-H100-80GB-HBM3"))
		Expect(captured).To(ContainSubstring("gpusPerNode: 4"),
			"the physical GPU count has to come from the shared counter sets, since gpu.count is 0 under MIG")
		Expect(captured).To(ContainSubstring("sharingMode: mig"))
		Expect(captured).To(ContainSubstring("nvlinkDomainSize: 2"),
			"topology exists only on device attributes, so this proves ResourceSlices were read")

		By("reproducing the per-GPU partition rather than the node totals")
		Expect(captured).To(ContainSubstring("profile: 3g.40gb"))
		Expect(captured).To(ContainSubstring("profile: 1g.10gb"))
		Expect(captured).NotTo(ContainSubstring("count: 16"),
			"the partition carries the node's 16 instances instead of the 4 per GPU that made them")
	})

	// Captured output is meant to be shared. Source node names carry internal
	// hostnames and topology, so leaking them would make the output unsafe to
	// paste into an issue — which is most of what it is for.
	It("does not carry source node names into its output", func() {
		Expect(captured).NotTo(ContainSubstring(sourceNodeA))
		Expect(captured).NotTo(ContainSubstring(sourceNodeB))
		Expect(captured).NotTo(ContainSubstring("capture-source"),
			"the source pool's own selector labels are internal detail too")
	})

	// The inverse of ghostgpu's safety invariant. Capture is pointed at
	// production by design, so "it only reads" has to be checked against a real
	// API server rather than argued from the code.
	It("writes nothing to the cluster it read", func() {
		nodeBefore := kubectlOut("get", "node", sourceNodeA, "-o", "jsonpath={.metadata.resourceVersion}")
		poolBefore := kubectlOut("get", "gpupool", sourcePool, "-o", "jsonpath={.metadata.resourceVersion}")
		nodesBefore := kubectlOut("get", "nodes", "-o", "name")

		_, _ = ghostgpuCapture()

		Expect(kubectlOut("get", "node", sourceNodeA, "-o", "jsonpath={.metadata.resourceVersion}")).
			To(Equal(nodeBefore), "capture modified a node it was only supposed to read")
		Expect(kubectlOut("get", "gpupool", sourcePool, "-o", "jsonpath={.metadata.resourceVersion}")).
			To(Equal(poolBefore), "capture modified the source pool")
		Expect(kubectlOut("get", "nodes", "-o", "name")).
			To(Equal(nodesBefore), "capture created or removed nodes")
	})

	// --nodes=false leaves the pools selecting a label nothing carries, so
	// capture has to say so — and has to say it somewhere that does not end up
	// in a redirected manifest file.
	It("reports what it could not reproduce on stderr, keeping stdout applyable", func() {
		out, warnings := ghostgpuCapture("--nodes=false")

		Expect(warnings).To(ContainSubstring("ghostgpu.dev/shape"))
		Expect(out).NotTo(ContainSubstring("warning:"),
			"a warning reached stdout, which would corrupt a redirected manifest file")
		Expect(out).NotTo(ContainSubstring("kind: Node"))
		Expect(strings.Count(out, "kind: GPUPool")).To(BeNumerically(">=", 1))
	})

	// The acceptance criterion: apply what capture printed and the cluster comes
	// back with the same fleet, from manifests nobody hand-wrote.
	It("round-trips: applying the capture reproduces the fleet", func() {
		Expect(applyYAML(captured)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", capturedPool, "{.status.nodesMatched}")).To(Equal("2"),
				"the emitted nodes were not adopted by the captured pool")
			g.Expect(jsonPath(g, "gpupool", capturedPool, "{.status.devicesPublished}")).
				To(Equal(sourceDevices), "the reproduced fleet is a different size from the one captured")
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("advertising the same extended resources as the cluster it came from")
		source := kubectlOut("get", "node", sourceNodeA, "-o",
			`jsonpath={.status.capacity.nvidia\.com/mig-1g\.10gb}`)
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "node", capturedModel+"-0",
				`{.status.capacity.nvidia\.com/mig-1g\.10gb}`)).To(Equal(source))
		}, time.Minute, 2*time.Second).Should(Succeed())
	})
})
