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
	"regexp"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
)

// profileNamePattern matches a MIG profile name and captures its two numbers.
// It is anchored on both ends so that trailing junk is rejected rather than
// silently ignored, and it mirrors the CRD's own pattern for the field.
var profileNamePattern = regexp.MustCompile(`^([0-9]+)g\.([0-9]+)gb$`)

// gibibyte is the unit the built-in tables use for profile memory.
const gibibyte = 1 << 30

// ParseProfileName derives a MIG instance's shape from its own name.
//
// NVIDIA profile names are not opaque identifiers: "3g.40gb" means three
// compute slices and a 40GB framebuffer. TestParseProfileNameAgreesWithBuiltInTables
// asserts that reading against every profile in every built-in table rather
// than taking it on faith, because the whole value of this function rests on
// the names really describing the shapes.
//
// That is what lets `ghostgpu capture` describe MIG on hardware ghostgpu has no
// table for. A cluster advertising nvidia.com/mig-2g.24gb has already told us
// everything needed to simulate it, so capture can reproduce that GPU without
// ghostgpu having to recognise the card.
//
// Names ghostgpu cannot read return false rather than a guess, matching
// ProfilesFor: inventing a shape would simulate hardware that does not exist.
func ParseProfileName(name string) (Profile, bool) {
	match := profileNamePattern.FindStringSubmatch(name)
	if match == nil {
		return Profile{}, false
	}

	// ParseInt with an explicit bit size: a profile name is arbitrary input,
	// and a wrapped slice count would describe hardware that cannot exist.
	slices64, err := strconv.ParseInt(match[1], 10, 32)
	slices := int32(slices64)
	if err != nil || slices64 < 1 {
		return Profile{}, false
	}
	memoryGiB, err := strconv.Atoi(match[2])
	if err != nil || memoryGiB < 1 {
		return Profile{}, false
	}

	return Profile{
		Name: name,
		// Built with NewQuantity rather than parsed from a formatted string, so
		// no input can reach a panicking MustParse.
		Memory: *resource.NewQuantity(int64(memoryGiB)*gibibyte, resource.BinarySI),
		Slices: slices,
	}, true
}
