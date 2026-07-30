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

package mig

import (
	"strings"
	"testing"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// Substrings the validation errors must contain, so a test asserting "the
// slices budget was exceeded" cannot pass on a message about memory.
const (
	errSlices = "slices"
	errMemory = "memory"
)

func partition(entries ...v1alpha1.MIGPartitionEntry) []v1alpha1.MIGPartitionEntry {
	return entries
}

func entry(profile string, count int32) v1alpha1.MIGPartitionEntry {
	return v1alpha1.MIGPartitionEntry{Profile: profile, Count: count}
}

func h100(t *testing.T) Table {
	t.Helper()
	table, ok := ProfilesFor(productH100)
	if !ok {
		t.Fatal("no built-in H100 table")
	}
	return table
}

// The whole point of a declared partition: everything in it coexists, so the
// instances must fit one GPU's budget. 2x3g.40gb is 6 of 7 slices and all 80Gi.
func TestValidatePartitionAcceptsAFittingLayout(t *testing.T) {
	cases := map[string][]v1alpha1.MIGPartitionEntry{
		"seven smallest":     partition(entry(p1g10gb, 7)),
		"one whole gpu":      partition(entry(p7g80gb, 1)),
		"two thirds":         partition(entry("3g.40gb", 2)),
		"mixed sizes":        partition(entry("3g.40gb", 1), entry(p1g10gb, 4)),
		"under-subscribed":   partition(entry(p1g10gb, 2)),
		"exactly the slices": partition(entry("4g.40gb", 1), entry("3g.40gb", 1)),
	}

	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePartition(entries, h100(t)); err != nil {
				t.Errorf("ValidatePartition: %v", err)
			}
		})
	}
}

func TestValidatePartitionRejectsOverBudget(t *testing.T) {
	cases := map[string]struct {
		entries []v1alpha1.MIGPartitionEntry
		wantErr string
	}{
		"too many slices": {
			// 8 x 1 slice exceeds the 7 available.
			entries: partition(entry(p1g10gb, 8)),
			wantErr: errSlices,
		},
		"too much memory": {
			// 5 x 20Gi is 100Gi against an 80Gi budget, and only 5 of 7 slices.
			entries: partition(entry("1g.20gb", 5)),
			wantErr: errMemory,
		},
		"two whole gpus": {
			entries: partition(entry(p7g80gb, 2)),
			wantErr: errSlices,
		},
		"combination overflows": {
			entries: partition(entry("4g.40gb", 1), entry("3g.40gb", 1), entry(p1g10gb, 1)),
			wantErr: errSlices,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidatePartition(tc.entries, h100(t))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}
}

// A profile the hardware does not have is the likeliest mistake, so the error
// names what is available rather than only what is wrong.
func TestValidatePartitionRejectsUnknownProfile(t *testing.T) {
	err := ValidatePartition(partition(entry("1g.5gb", 1)), h100(t))
	if err == nil {
		t.Fatal("expected an error for a profile this GPU does not support")
	}
	if !strings.Contains(err.Error(), "1g.5gb") {
		t.Errorf("error should name the bad profile: %v", err)
	}
	if !strings.Contains(err.Error(), p1g10gb) {
		t.Errorf("error should list available profiles: %v", err)
	}
}

func TestValidatePartitionEmptyIsAllowed(t *testing.T) {
	if err := ValidatePartition(nil, h100(t)); err != nil {
		t.Errorf("an empty partition means dynamic MIG and must be allowed: %v", err)
	}
}

// Static MIG creates every declared instance, so expansion must produce them
// all — including several of one profile, which is what forces an ordinal into
// the device name.
func TestExpandPartitionedProducesEveryInstance(t *testing.T) {
	entries := partition(entry("3g.40gb", 1), entry(p1g10gb, 4))

	instances := ExpandPartitioned(2, h100(t), entries)

	// 2 GPUs x (1 + 4) instances.
	if len(instances) != 10 {
		t.Fatalf("got %d instances, want 10", len(instances))
	}

	names := map[string]struct{}{}
	for _, in := range instances {
		if _, dup := names[in.DeviceName]; dup {
			t.Errorf("duplicate device name %q; instances of one profile must be distinguishable",
				in.DeviceName)
		}
		names[in.DeviceName] = struct{}{}
	}

	for _, want := range []string{
		"gpu-0-3g-40gb-0",
		"gpu-0-1g-10gb-0", "gpu-0-1g-10gb-3",
		"gpu-1-3g-40gb-0", "gpu-1-1g-10gb-3",
	} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing expected device %q; got %v", want, names)
		}
	}
}

// Every declared instance holds its share of the same GPU's budget, and because
// the layout fits by construction they can all be allocated at once. That is
// exactly what makes the scalar projection exact under a partition.
func TestExpandPartitionedInstancesShareTheirGPUsCounterSet(t *testing.T) {
	instances := ExpandPartitioned(1, h100(t), partition(entry(p1g10gb, 3)))

	for _, in := range instances {
		if in.CounterSet != CounterSetName(0) {
			t.Errorf("device %s draws on %q, want %q", in.DeviceName, in.CounterSet, CounterSetName(0))
		}
		if in.Profile.Slices != 1 {
			t.Errorf("device %s consumes %d slices, want 1", in.DeviceName, in.Profile.Slices)
		}
	}
}

// Two instances of one profile on one card are different instances, so their
// GPU_I_ID must differ or the v0.3 exporter would merge their metrics.
func TestExpandPartitionedInstanceIDsAreUniquePerGPU(t *testing.T) {
	instances := ExpandPartitioned(2, h100(t), partition(entry("3g.40gb", 2), entry(p1g10gb, 1)))

	perGPU := map[int32]map[int32]string{}
	for _, in := range instances {
		if perGPU[in.GPUIndex] == nil {
			perGPU[in.GPUIndex] = map[int32]string{}
		}
		if other, dup := perGPU[in.GPUIndex][in.InstanceID]; dup {
			t.Errorf("gpu %d: %s and %s share instance ID %d",
				in.GPUIndex, other, in.DeviceName, in.InstanceID)
		}
		perGPU[in.GPUIndex][in.InstanceID] = in.DeviceName
	}
}

func TestExpandPartitionedIsDeterministic(t *testing.T) {
	entries := partition(entry("3g.40gb", 1), entry(p1g10gb, 4))

	first := ExpandPartitioned(4, h100(t), entries)
	second := ExpandPartitioned(4, h100(t), entries)

	if len(first) != len(second) {
		t.Fatalf("length differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].DeviceName != second[i].DeviceName {
			t.Errorf("instance %d differs: %q vs %q", i, first[i].DeviceName, second[i].DeviceName)
		}
	}
}

// Ordering follows the table rather than the order entries were written, so the
// same layout always produces the same slices however the YAML was arranged.
func TestExpandPartitionedOrderIsIndependentOfEntryOrder(t *testing.T) {
	a := ExpandPartitioned(1, h100(t), partition(entry(p7g80gb, 1)))
	b := ExpandPartitioned(1, h100(t), partition(entry(p7g80gb, 1)))
	if a[0].DeviceName != b[0].DeviceName {
		t.Fatal("identical partitions produced different names")
	}

	small := partition(entry(p1g10gb, 1), entry("3g.40gb", 1))
	large := partition(entry("3g.40gb", 1), entry(p1g10gb, 1))

	first := ExpandPartitioned(1, h100(t), small)
	second := ExpandPartitioned(1, h100(t), large)

	for i := range first {
		if first[i].DeviceName != second[i].DeviceName {
			t.Errorf("entry order changed device %d: %q vs %q",
				i, first[i].DeviceName, second[i].DeviceName)
		}
	}
}

func TestExpandPartitionedZeroGPUs(t *testing.T) {
	if got := ExpandPartitioned(0, h100(t), partition(entry(p1g10gb, 1))); len(got) != 0 {
		t.Errorf("got %d instances for 0 GPUs, want none", len(got))
	}
}
