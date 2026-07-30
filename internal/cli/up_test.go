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

package cli

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

const (
	testModelName = "h100"
	testPoolName  = "h100-pool"
	nodeTypeKey   = "type"
)

func validOptions() UpOptions {
	return UpOptions{
		Name:             testModelName,
		Product:          "NVIDIA-H100-SXM",
		Memory:           "80Gi",
		Compute:          "9.0",
		GPUsPerNode:      8,
		NVLinkDomainSize: 4,
		NodeSelector:     "type=kwok",
		DRA:              true,
		ExtendedResource: true,
	}
}

func TestBuildManifestsProducesModelAndPool(t *testing.T) {
	objs, err := BuildManifests(validOptions())
	if err != nil {
		t.Fatalf("BuildManifests: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2 (GPUModel + GPUPool)", len(objs))
	}

	model, ok := objs[0].(*v1alpha1.GPUModel)
	if !ok {
		t.Fatalf("first object is %T, want *GPUModel", objs[0])
	}
	if model.Name != testModelName {
		t.Errorf("model name = %q, want h100", model.Name)
	}
	if model.Spec.ProductName != "NVIDIA-H100-SXM" {
		t.Errorf("productName = %q", model.Spec.ProductName)
	}
	if model.Spec.Memory.Cmp(resource.MustParse("80Gi")) != 0 {
		t.Errorf("memory = %v, want 80Gi", model.Spec.Memory.String())
	}

	pool, ok := objs[1].(*v1alpha1.GPUPool)
	if !ok {
		t.Fatalf("second object is %T, want *GPUPool", objs[1])
	}
	if pool.Spec.ModelRef != model.Name {
		t.Errorf("pool.modelRef = %q, want %q", pool.Spec.ModelRef, model.Name)
	}
	if pool.Spec.GPUsPerNode != 8 {
		t.Errorf("gpusPerNode = %d, want 8", pool.Spec.GPUsPerNode)
	}
	if pool.Spec.NodeSelector[nodeTypeKey] != "kwok" {
		t.Errorf("nodeSelector = %v", pool.Spec.NodeSelector)
	}
}

// TypeMeta is empty on objects built in Go. Without it, rendered YAML has no
// apiVersion or kind and `kubectl apply -f -` rejects it, which would make
// --dry-run useless for its main purpose.
func TestBuildManifestsSetsTypeMeta(t *testing.T) {
	objs, err := BuildManifests(validOptions())
	if err != nil {
		t.Fatal(err)
	}

	wantKinds := []string{"GPUModel", "GPUPool"}
	for i, want := range wantKinds {
		gvk := objs[i].GetObjectKind().GroupVersionKind()
		if gvk.Kind != want {
			t.Errorf("object %d kind = %q, want %q", i, gvk.Kind, want)
		}
		if got := gvk.GroupVersion().String(); got != "ghostgpu.dev/v1alpha1" {
			t.Errorf("object %d apiVersion = %q, want ghostgpu.dev/v1alpha1", i, got)
		}
	}
}

// The advertise fields are pointers precisely so that an explicit false
// survives. A CLI that sends nil for --dra=false would have the API server
// default it straight back to true.
func TestBuildManifestsAdvertiseTogglesAreExplicit(t *testing.T) {
	opts := validOptions()
	opts.DRA = false
	opts.ExtendedResource = true

	objs, err := BuildManifests(opts)
	if err != nil {
		t.Fatal(err)
	}
	pool := objs[1].(*v1alpha1.GPUPool)

	if pool.Spec.Advertise.DRA == nil {
		t.Fatal("advertise.dra is nil; an explicit false would be defaulted back to true")
	}
	if *pool.Spec.Advertise.DRA {
		t.Error("advertise.dra = true, want false")
	}
	if pool.Spec.Advertise.ExtendedResource == nil || !*pool.Spec.Advertise.ExtendedResource {
		t.Error("advertise.extendedResource should be explicitly true")
	}
}

func TestBuildManifestsRejectsInvalidInput(t *testing.T) {
	cases := map[string]func(*UpOptions){
		"bad memory":      func(o *UpOptions) { o.Memory = "not-a-quantity" },
		"empty name":      func(o *UpOptions) { o.Name = "" },
		"zero gpus":       func(o *UpOptions) { o.GPUsPerNode = 0 },
		"too many gpus":   func(o *UpOptions) { o.GPUsPerNode = 129 },
		"bad selector":    func(o *UpOptions) { o.NodeSelector = nodeTypeKey },
		"empty product":   func(o *UpOptions) { o.Product = "" },
		"bad capability":  func(o *UpOptions) { o.Compute = "nine" },
		"negative nvlink": func(o *UpOptions) { o.NVLinkDomainSize = -1 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := validOptions()
			mutate(&opts)
			if _, err := BuildManifests(opts); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// 128 is the DRA per-slice device limit and the CRD's maximum. The boundary
// must be allowed, not rejected off by one.
func TestBuildManifestsAllowsMaximumGPUs(t *testing.T) {
	opts := validOptions()
	opts.GPUsPerNode = 128

	if _, err := BuildManifests(opts); err != nil {
		t.Errorf("128 GPUs per node rejected: %v", err)
	}
}

func TestBuildManifestsOmitsNVLinkWhenZero(t *testing.T) {
	opts := validOptions()
	opts.NVLinkDomainSize = 0

	objs, err := BuildManifests(opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := objs[1].(*v1alpha1.GPUPool).Spec.Topology.NVLinkDomainSize; got != 0 {
		t.Errorf("nvlinkDomainSize = %d, want 0", got)
	}
}

func TestParseSelector(t *testing.T) {
	t.Run("multiple pairs", func(t *testing.T) {
		got, err := ParseSelector("type=kwok,zone=us-east-1a")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{nodeTypeKey: "kwok", "zone": "us-east-1a"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("got[%q] = %q, want %q", k, got[k], v)
			}
		}
	})

	// An empty selector means "every simulated node", which is a reasonable
	// default and must not be an error.
	t.Run("empty is allowed", func(t *testing.T) {
		got, err := ParseSelector("")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("rejects malformed pairs", func(t *testing.T) {
		for _, s := range []string{"type", "=kwok", "type=", "a=b,c"} {
			if _, err := ParseSelector(s); err == nil {
				t.Errorf("ParseSelector(%q) succeeded, want error", s)
			}
		}
	})
}

func TestRenderYAML(t *testing.T) {
	objs, err := BuildManifests(validOptions())
	if err != nil {
		t.Fatal(err)
	}

	out, err := RenderYAML(objs)
	if err != nil {
		t.Fatalf("RenderYAML: %v", err)
	}

	for _, want := range []string{
		"apiVersion: ghostgpu.dev/v1alpha1",
		"kind: GPUModel",
		"kind: GPUPool",
		"productName: NVIDIA-H100-SXM",
		"gpusPerNode: 8",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered YAML missing %q:\n%s", want, out)
		}
	}

	// Two documents, so the output is directly pipeable into kubectl apply -f -.
	if got := strings.Count(out, "---"); got != 1 {
		t.Errorf("got %d document separators, want 1 (two documents):\n%s", got, out)
	}
}
