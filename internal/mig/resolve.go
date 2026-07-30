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
	"fmt"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// Resolve produces the MIG table a GPUModel describes, filling in ghostgpu's
// built-in profile set when the model does not declare one.
//
// The budget's memory defaults to the *model's* memory rather than the built-in
// table's. Hardware can share a profile family without sharing a capacity — an
// H100 NVL carries 94GB against the H100 table's 80GB — and inheriting the
// table's figure would quietly simulate a different card. Taking it from the
// model also means a mismatch surfaces as a validation error here rather than
// as devices that can never be allocated.
func Resolve(model *v1alpha1.GPUModel) (Table, error) {
	table := baseTable(model)

	if len(table.Profiles) == 0 {
		return Table{}, fmt.Errorf(
			"GPUModel %q: no built-in MIG profiles for product %q; set spec.migProfiles explicitly",
			model.Name, model.Spec.ProductName)
	}

	if err := validate(model.Name, table); err != nil {
		return Table{}, err
	}
	return table, nil
}

// baseTable assembles the profile list and budget from the model, consulting
// the built-in tables only for what the model leaves unspecified.
func baseTable(model *v1alpha1.GPUModel) Table {
	builtIn, known := ProfilesFor(model.Spec.ProductName)

	table := Table{
		Budget: Budget{
			// Default: the GPU's own memory, and the slice count of whatever
			// hardware family this product belongs to.
			Memory: model.Spec.Memory.DeepCopy(),
			Slices: defaultSlices,
		},
	}
	if known {
		table.Budget.Slices = builtIn.Budget.Slices
	}

	switch {
	case len(model.Spec.MIGProfiles) > 0:
		table.Profiles = make([]Profile, 0, len(model.Spec.MIGProfiles))
		for _, p := range model.Spec.MIGProfiles {
			table.Profiles = append(table.Profiles, Profile{
				Name:   p.Name,
				Memory: p.Memory.DeepCopy(),
				Slices: p.Slices,
			})
		}
	case known:
		table.Profiles = builtIn.Profiles
	}

	if b := model.Spec.MIGBudget; b != nil {
		if b.Memory != nil {
			table.Budget.Memory = b.Memory.DeepCopy()
		}
		if b.Slices > 0 {
			table.Budget.Slices = b.Slices
		}
	}

	return table
}

// defaultSlices is the compute-slice count of every MIG-capable NVIDIA GPU to
// date. It applies when a model names an unknown product but supplies its own
// profiles and no explicit budget.
const defaultSlices = 7

func validate(modelName string, table Table) error {
	seen := make(map[string]struct{}, len(table.Profiles))

	for _, p := range table.Profiles {
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("GPUModel %q: duplicate MIG profile %q", modelName, p.Name)
		}
		seen[p.Name] = struct{}{}

		if p.Slices > table.Budget.Slices {
			return fmt.Errorf(
				"GPUModel %q: MIG profile %q consumes %d slices but the budget is %d; it could never be allocated",
				modelName, p.Name, p.Slices, table.Budget.Slices)
		}
		if p.Memory.Cmp(table.Budget.Memory) > 0 {
			return fmt.Errorf(
				"GPUModel %q: MIG profile %q consumes %s memory but the budget is %s; it could never be allocated",
				modelName, p.Name, p.Memory.String(), table.Budget.Memory.String())
		}
	}
	return nil
}
