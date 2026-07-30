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
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
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
