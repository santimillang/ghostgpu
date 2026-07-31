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

	profileSmall = "1g.10gb"
	profileMid   = "3g.40gb"
	// A real A100 profile, which makes it a plausible typo rather than
	// nonsense — exactly the mistake the error message has to handle well.
	profileWrongHardware = "1g.5gb"
)

func validOptions() UpOptions {
	return UpOptions{
		Name:             testModelName,
		Product:          "NVIDIA-H100-SXM",
		Memory:           h100Memory,
		Compute:          h100Compute,
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
	if model.Spec.Memory.Cmp(resource.MustParse(h100Memory)) != 0 {
		t.Errorf("memory = %v, want %s", model.Spec.Memory.String(), h100Memory)
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
	if pool.Spec.NodeSelector[nodeTypeKey] != nodeTypeKwok {
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

	wantKinds := []string{kindGPUModel, kindGPUPool}
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

func migOptions() UpOptions {
	opts := validOptions()
	opts.Product = "NVIDIA-H100-80GB-HBM3"
	opts.SharingMode = string(v1alpha1.SharingModeMIG)
	return opts
}

func TestBuildManifestsMIGUsesBuiltInProfiles(t *testing.T) {
	objs, err := BuildManifests(migOptions())
	if err != nil {
		t.Fatalf("BuildManifests: %v", err)
	}

	model := objs[0].(*v1alpha1.GPUModel)
	pool := objs[1].(*v1alpha1.GPUPool)

	if !pool.Spec.MIGEnabled() {
		t.Error("sharingMode was not set to mig")
	}
	// Left empty on purpose: the operator resolves the built-in table for the
	// product, so the emitted manifest stays short and stays correct if the
	// table gains a profile.
	if len(model.Spec.MIGProfiles) != 0 {
		t.Errorf("migProfiles = %v, want empty so the built-in table is used",
			model.Spec.MIGProfiles)
	}
}

// Naming profiles restricts a known GPU to a subset. The full shape has to be
// written into the manifest, because a bare name carries no memory or slice
// count and the operator would otherwise have to guess which table it came from.
func TestBuildManifestsMIGProfileSubset(t *testing.T) {
	opts := migOptions()
	opts.MIGProfiles = "1g.10gb,3g.40gb"

	objs, err := BuildManifests(opts)
	if err != nil {
		t.Fatalf("BuildManifests: %v", err)
	}
	model := objs[0].(*v1alpha1.GPUModel)

	if len(model.Spec.MIGProfiles) != 2 {
		t.Fatalf("got %d profiles, want 2: %v", len(model.Spec.MIGProfiles), model.Spec.MIGProfiles)
	}
	got := []string{model.Spec.MIGProfiles[0].Name, model.Spec.MIGProfiles[1].Name}
	if got[0] != profileSmall || got[1] != profileMid {
		t.Errorf("profiles = %v, want [1g.10gb 3g.40gb]", got)
	}

	// The consumption must survive, or the published devices would draw down
	// nothing and every profile could be allocated at once.
	if model.Spec.MIGProfiles[0].Slices != 1 {
		t.Errorf("1g.10gb consumes %d slices, want 1", model.Spec.MIGProfiles[0].Slices)
	}
	if model.Spec.MIGProfiles[1].Slices != 3 {
		t.Errorf("3g.40gb consumes %d slices, want 3", model.Spec.MIGProfiles[1].Slices)
	}
	if model.Spec.MIGProfiles[0].Memory.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Errorf("1g.10gb memory = %v, want 10Gi", model.Spec.MIGProfiles[0].Memory.String())
	}
}

// Selection follows the table's order rather than the order typed, so the same
// set always produces the same manifest regardless of how it was written.
func TestBuildManifestsMIGProfileOrderIsStable(t *testing.T) {
	first := migOptions()
	first.MIGProfiles = "7g.80gb,1g.10gb"
	second := migOptions()
	second.MIGProfiles = "1g.10gb,7g.80gb"

	a, err := BuildManifests(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildManifests(second)
	if err != nil {
		t.Fatal(err)
	}

	profilesA := a[0].(*v1alpha1.GPUModel).Spec.MIGProfiles
	profilesB := b[0].(*v1alpha1.GPUModel).Spec.MIGProfiles
	for i := range profilesA {
		if profilesA[i].Name != profilesB[i].Name {
			t.Errorf("profile %d differs by input order: %q vs %q",
				i, profilesA[i].Name, profilesB[i].Name)
		}
	}
}

func TestBuildManifestsMIGRejectsBadInput(t *testing.T) {
	cases := map[string]struct {
		mutate  func(*UpOptions)
		wantErr string
	}{
		"unknown sharing mode": {
			mutate:  func(o *UpOptions) { o.SharingMode = "timeSlicing" },
			wantErr: "sharing-mode",
		},
		"unknown product with no table": {
			mutate:  func(o *UpOptions) { o.Product = "NVIDIA-GTX-1080" },
			wantErr: "migProfiles",
		},
		"profile not on this hardware": {
			mutate:  func(o *UpOptions) { o.MIGProfiles = profileWrongHardware },
			wantErr: profileWrongHardware,
		},
		"profiles without mig mode": {
			mutate: func(o *UpOptions) {
				o.SharingMode = "none"
				o.MIGProfiles = profileSmall
			},
			wantErr: "sharing-mode",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			opts := migOptions()
			tc.mutate(&opts)

			_, err := BuildManifests(opts)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}
}

// An unknown profile name is the most likely typo, so the error has to say what
// is available rather than only what is wrong.
func TestBuildManifestsMIGErrorListsAvailableProfiles(t *testing.T) {
	opts := migOptions()
	opts.MIGProfiles = profileWrongHardware

	_, err := BuildManifests(opts)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, available := range []string{profileSmall, "7g.80gb"} {
		if !strings.Contains(err.Error(), available) {
			t.Errorf("error should list available profile %q: %v", available, err)
		}
	}
}

func TestRenderYAMLIncludesMIGFields(t *testing.T) {
	opts := migOptions()
	opts.MIGProfiles = profileSmall

	objs, err := BuildManifests(opts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderYAML(objs)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"sharingMode: mig", "migProfiles:", "name: 1g.10gb", "slices: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered YAML missing %q:\n%s", want, out)
		}
	}
}

func TestParsePartition(t *testing.T) {
	t.Run("profile=count pairs", func(t *testing.T) {
		got, err := ParsePartition("3g.40gb=1,1g.10gb=4")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2: %v", len(got), got)
		}
		if got[0].Profile != profileMid || got[0].Count != 1 {
			t.Errorf("entry 0 = %+v, want {3g.40gb 1}", got[0])
		}
		if got[1].Profile != profileSmall || got[1].Count != 4 {
			t.Errorf("entry 1 = %+v, want {1g.10gb 4}", got[1])
		}
	})

	// A bare profile means one of it, which is the common case and reads
	// better than forcing "=1" everywhere.
	t.Run("bare profile means one", func(t *testing.T) {
		got, err := ParsePartition("7g.80gb")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Count != 1 {
			t.Errorf("got %+v, want a single entry with count 1", got)
		}
	})

	t.Run("empty means dynamic MIG", func(t *testing.T) {
		got, err := ParsePartition("")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want no entries", got)
		}
	})

	t.Run("rejects malformed input", func(t *testing.T) {
		for _, s := range []string{
			"3g.40gb=",    // no count
			"3g.40gb=x",   // count is not a number
			"3g.40gb=0",   // an instance that does not exist is not a partition
			"3g.40gb=-1",  // negative
			"=4",          // no profile
			"3g.40gb=1,,", // stray separator
			"3g.40gb=1=2", // two counts
		} {
			if _, err := ParsePartition(s); err == nil {
				t.Errorf("ParsePartition(%q) succeeded, want an error", s)
			}
		}
	})

	// Repeating a profile is ambiguous rather than additive: the API stores it
	// as a list keyed by profile, so two entries could not both survive.
	t.Run("rejects a repeated profile", func(t *testing.T) {
		if _, err := ParsePartition("1g.10gb=2,1g.10gb=3"); err == nil {
			t.Error("expected an error for a repeated profile")
		}
	})
}

func TestBuildManifestsCarriesPartition(t *testing.T) {
	opts := migOptions()
	opts.MIGPartition = "3g.40gb=1,1g.10gb=4"

	objs, err := BuildManifests(opts)
	if err != nil {
		t.Fatalf("BuildManifests: %v", err)
	}
	pool := objs[1].(*v1alpha1.GPUPool)

	if len(pool.Spec.MIGPartition) != 2 {
		t.Fatalf("got %d partition entries, want 2", len(pool.Spec.MIGPartition))
	}
	if pool.Spec.MIGPartition[1].Count != 4 {
		t.Errorf("1g.10gb count = %d, want 4", pool.Spec.MIGPartition[1].Count)
	}
}

// The CLI validates against the same budget the operator does, so a layout
// that cannot fit is rejected before it reaches a cluster — and --dry-run,
// which never reaches one at all, still catches it.
func TestBuildManifestsRejectsUnfittablePartition(t *testing.T) {
	opts := migOptions()
	opts.MIGPartition = "7g.80gb=2"

	_, err := BuildManifests(opts)
	if err == nil {
		t.Fatal("expected an error for a partition that cannot fit one GPU")
	}
	if !strings.Contains(err.Error(), "slices") {
		t.Errorf("error should explain which budget was exceeded: %v", err)
	}
}

// A partition is validated against the profiles the pool actually exposes, not
// against everything the hardware supports. Restricting the profiles and then
// declaring one that was excluded has to fail, or the pool would advertise
// instances it never publishes.
func TestBuildManifestsPartitionRespectsProfileSubset(t *testing.T) {
	opts := migOptions()
	opts.MIGProfiles = profileSmall
	opts.MIGPartition = profileMid + "=1"

	_, err := BuildManifests(opts)
	if err == nil {
		t.Fatal("expected an error: the partition names a profile the pool does not expose")
	}
	if !strings.Contains(err.Error(), profileMid) {
		t.Errorf("error should name the excluded profile: %v", err)
	}
}

func TestBuildManifestsPartitionWithinProfileSubset(t *testing.T) {
	opts := migOptions()
	opts.MIGProfiles = profileSmall + "," + profileMid
	opts.MIGPartition = profileMid + "=1," + profileSmall + "=4"

	objs, err := BuildManifests(opts)
	if err != nil {
		t.Fatalf("BuildManifests: %v", err)
	}
	if got := len(objs[1].(*v1alpha1.GPUPool).Spec.MIGPartition); got != 2 {
		t.Errorf("got %d partition entries, want 2", got)
	}
}

func TestBuildManifestsPartitionRequiresMIG(t *testing.T) {
	opts := validOptions()
	opts.MIGPartition = "1g.10gb=1"

	if _, err := BuildManifests(opts); err == nil {
		t.Error("expected an error for a partition without sharing-mode mig")
	}
}

func TestParseSelector(t *testing.T) {
	t.Run("multiple pairs", func(t *testing.T) {
		got, err := ParseSelector("type=kwok,zone=us-east-1a")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{nodeTypeKey: nodeTypeKwok, "zone": "us-east-1a"}
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
