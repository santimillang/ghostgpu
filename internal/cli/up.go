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

// Package cli implements the ghostgpu command-line interface.
//
// Everything here is a pure function of its options: building the manifests
// never contacts a cluster. That is what makes --dry-run trustworthy and the
// whole surface unit-testable without an API server.
package cli

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// MaxGPUsPerNode mirrors the GPUPool CRD maximum, which exists because DRA
// permits at most 128 devices in one ResourceSlice.
const MaxGPUsPerNode = 128

// computeCapabilityPattern mirrors the GPUModel CRD validation.
var computeCapabilityPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// UpOptions describes a one-command simulated GPU fleet.
type UpOptions struct {
	Name             string
	Product          string
	Memory           string
	Compute          string
	GPUsPerNode      int32
	NVLinkDomainSize int32
	NUMAAware        bool
	NodeSelector     string
	DRA              bool
	ExtendedResource bool

	// SharingMode is "none" or "mig". Empty means "none".
	SharingMode string

	// MIGProfiles optionally restricts a MIG pool to a subset of the profiles
	// its hardware supports, as a comma-separated list of profile names.
	MIGProfiles string

	// MIGPartition optionally declares which MIG instances actually exist, as
	// "profile=count" pairs. Empty models dynamic MIG.
	MIGPartition string
}

// ParsePartition turns a "3g.40gb=1,1g.10gb=4" flag value into partition
// entries.
//
// A bare profile name means one instance, which is the common case and reads
// better than requiring "=1" everywhere.
func ParsePartition(s string) ([]v1alpha1.MIGPartitionEntry, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}

	var entries []v1alpha1.MIGPartitionEntry
	seen := map[string]bool{}

	for pair := range strings.SplitSeq(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return nil, fmt.Errorf("invalid partition %q: empty entry", s)
		}

		profile, countText, hasCount := strings.Cut(pair, "=")
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return nil, fmt.Errorf("invalid partition %q: entry %q names no profile", s, pair)
		}
		if seen[profile] {
			// The API stores this as a list keyed by profile, so two entries
			// for one profile could not both survive. Summing them silently
			// would be a guess at what the user meant.
			return nil, fmt.Errorf("invalid partition %q: profile %q appears twice", s, profile)
		}
		seen[profile] = true

		count := int64(1)
		if hasCount {
			parsed, err := strconv.ParseInt(strings.TrimSpace(countText), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid partition %q: count for %q is not a number", s, profile)
			}
			if parsed < 1 {
				return nil, fmt.Errorf(
					"invalid partition %q: count for %q is %d; an instance that does not exist is not part of a partition",
					s, profile, parsed)
			}
			count = parsed
		}

		entries = append(entries, v1alpha1.MIGPartitionEntry{
			Profile: profile,
			Count:   int32(count),
		})
	}

	return entries, nil
}

// migSelection resolves the profiles a MIG pool should expose.
//
// A profile name on its own carries no memory or slice count, so a named subset
// has to be expanded from the built-in table for the product. That is also why
// the CLI can only restrict profiles for hardware ghostgpu knows: inventing the
// consumption for an unrecognised GPU would simulate something that does not
// exist. Users with such hardware write migProfiles in YAML, which the error
// says.
//
// The result follows the table's order rather than the order typed, so the same
// set always produces the same manifest.
func migSelection(opts UpOptions) ([]v1alpha1.MIGProfileSpec, error) {
	table, known := mig.ProfilesFor(opts.Product)
	if !known {
		return nil, fmt.Errorf(
			"no built-in MIG profiles for %q; declare spec.migProfiles in a GPUModel manifest instead",
			opts.Product)
	}

	// Empty means "everything this hardware supports", and is left out of the
	// manifest entirely so the operator resolves the same table. Writing the
	// profiles out would freeze today's table into the user's YAML.
	if strings.TrimSpace(opts.MIGProfiles) == "" {
		return nil, nil
	}

	wanted := map[string]bool{}
	for name := range strings.SplitSeq(opts.MIGProfiles, ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}

	selected := make([]v1alpha1.MIGProfileSpec, 0, len(wanted))
	for _, p := range table.Profiles {
		if wanted[p.Name] {
			selected = append(selected, v1alpha1.MIGProfileSpec{
				Name:   p.Name,
				Memory: p.Memory,
				Slices: p.Slices,
			})
			delete(wanted, p.Name)
		}
	}

	if len(wanted) > 0 {
		available := make([]string, 0, len(table.Profiles))
		for _, p := range table.Profiles {
			available = append(available, p.Name)
		}
		missing := slices.Sorted(maps.Keys(wanted))
		return nil, fmt.Errorf("%s does not support MIG profile(s) %s; available: %s",
			opts.Product, strings.Join(missing, ", "), strings.Join(available, ", "))
	}

	return selected, nil
}

// toMIGProfiles converts the API's profile shape back into the mig package's,
// so a partition can be validated against a user-restricted profile set rather
// than against everything the hardware supports.
func toMIGProfiles(specs []v1alpha1.MIGProfileSpec) []mig.Profile {
	out := make([]mig.Profile, 0, len(specs))
	for _, s := range specs {
		out = append(out, mig.Profile{Name: s.Name, Memory: s.Memory, Slices: s.Slices})
	}
	return out
}

// ParseSelector turns a "k=v,k2=v2" flag value into a label map.
//
// An empty string yields an empty map rather than an error: a pool with no
// selector matches every simulated node, which is a reasonable default.
func ParseSelector(s string) (map[string]string, error) {
	labels := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return labels, nil
	}

	for pair := range strings.SplitSeq(s, ",") {
		key, value, ok := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("invalid selector %q: want comma-separated key=value pairs", s)
		}
		labels[key] = value
	}
	return labels, nil
}

// BuildManifests turns CLI options into the GPUModel/GPUPool pair to apply.
//
// The validation here duplicates part of the CRD schema on purpose. --dry-run
// never reaches an API server, so without it a user would only learn that
// gpusPerNode is out of range once they piped the output into a cluster. The
// API server remains the authority; this is an early, friendlier copy.
func BuildManifests(opts UpOptions) ([]client.Object, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if opts.Product == "" {
		return nil, fmt.Errorf("gpu product name is required")
	}
	if opts.GPUsPerNode < 1 || opts.GPUsPerNode > MaxGPUsPerNode {
		return nil, fmt.Errorf("gpus-per-node must be between 1 and %d, got %d",
			MaxGPUsPerNode, opts.GPUsPerNode)
	}
	if opts.NVLinkDomainSize < 0 {
		return nil, fmt.Errorf("nvlink-domain-size must not be negative, got %d", opts.NVLinkDomainSize)
	}
	if !computeCapabilityPattern.MatchString(opts.Compute) {
		return nil, fmt.Errorf("invalid compute capability %q: want <major>.<minor>, e.g. 9.0", opts.Compute)
	}

	memory, err := resource.ParseQuantity(opts.Memory)
	if err != nil {
		return nil, fmt.Errorf("invalid memory %q: %w", opts.Memory, err)
	}

	sharingMode := v1alpha1.SharingMode(opts.SharingMode)
	if opts.SharingMode == "" {
		sharingMode = v1alpha1.SharingModeNone
	}
	if sharingMode != v1alpha1.SharingModeNone && sharingMode != v1alpha1.SharingModeMIG {
		return nil, fmt.Errorf("invalid sharing-mode %q: want none or mig", opts.SharingMode)
	}

	var (
		profiles  []v1alpha1.MIGProfileSpec
		partition []v1alpha1.MIGPartitionEntry
	)
	switch {
	case sharingMode == v1alpha1.SharingModeMIG:
		if profiles, err = migSelection(opts); err != nil {
			return nil, err
		}
		if partition, err = ParsePartition(opts.MIGPartition); err != nil {
			return nil, err
		}
		// Validated here against the same budget the operator uses, so a
		// layout that cannot fit is caught before it reaches a cluster — and
		// caught by --dry-run, which never reaches one at all.
		if len(partition) > 0 {
			table, known := mig.ProfilesFor(opts.Product)
			if known {
				if len(profiles) > 0 {
					table.Profiles = toMIGProfiles(profiles)
				}
				if err = mig.ValidatePartition(partition, table); err != nil {
					return nil, err
				}
			}
		}
	case opts.MIGProfiles != "":
		return nil, fmt.Errorf("mig-profiles requires sharing-mode mig")
	case opts.MIGPartition != "":
		return nil, fmt.Errorf("mig-partition requires sharing-mode mig")
	}

	selector, err := ParseSelector(opts.NodeSelector)
	if err != nil {
		return nil, err
	}

	// TypeMeta is set explicitly. Objects constructed in Go carry an empty one,
	// and without apiVersion/kind the rendered YAML is not applyable.
	model := &v1alpha1.GPUModel{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "GPUModel",
		},
		ObjectMeta: metav1.ObjectMeta{Name: opts.Name},
		Spec: v1alpha1.GPUModelSpec{
			Vendor:            "nvidia",
			ProductName:       opts.Product,
			Memory:            memory,
			ComputeCapability: opts.Compute,
			MIGProfiles:       profiles,
		},
	}

	pool := &v1alpha1.GPUPool{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "GPUPool",
		},
		ObjectMeta: metav1.ObjectMeta{Name: opts.Name + "-pool"},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef:     opts.Name,
			NodeSelector: selector,
			GPUsPerNode:  opts.GPUsPerNode,
			// Written explicitly rather than left nil. These fields are
			// pointers so that an explicit false survives defaulting; sending
			// nil for --dra=false would have the API server set it back to true.
			Advertise: v1alpha1.AdvertiseSpec{
				DRA:              ptr.To(opts.DRA),
				ExtendedResource: ptr.To(opts.ExtendedResource),
			},
			SharingMode:  sharingMode,
			MIGPartition: partition,
			Topology: v1alpha1.TopologySpec{
				NVLinkDomainSize: opts.NVLinkDomainSize,
				NUMAAware:        opts.NUMAAware,
			},
		},
	}

	return []client.Object{model, pool}, nil
}

// RenderYAML renders objects as a multi-document YAML stream suitable for
// piping straight into kubectl apply -f -.
func RenderYAML(objs []client.Object) (string, error) {
	docs := make([]string, 0, len(objs))
	for _, obj := range objs {
		out, err := yaml.Marshal(obj)
		if err != nil {
			return "", fmt.Errorf("marshaling %s: %w", obj.GetName(), err)
		}
		docs = append(docs, string(out))
	}
	return strings.Join(docs, "---\n"), nil
}
