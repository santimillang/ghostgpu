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
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// Scheme returns a scheme with the ghostgpu API types registered.
func Scheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Apply creates each object, updating it if it already exists, and returns one
// human-readable line per object describing what happened.
//
// Running the same command twice must succeed rather than fail on
// AlreadyExists: re-running with adjusted flags is the obvious way to change a
// simulated fleet, and a CLI that only ever creates would force users to delete
// first.
func Apply(ctx context.Context, c client.Client, objs []client.Object) ([]string, error) {
	results := make([]string, 0, len(objs))

	for _, obj := range objs {
		kind := strings.ToLower(obj.GetObjectKind().GroupVersionKind().Kind)

		existing, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return results, fmt.Errorf("%s/%s is not a client.Object", kind, obj.GetName())
		}

		err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing)
		switch {
		case apierrors.IsNotFound(err):
			if err := c.Create(ctx, obj); err != nil {
				return results, fmt.Errorf("creating %s/%s: %w", kind, obj.GetName(), err)
			}
			results = append(results, fmt.Sprintf("%s/%s created", kind, obj.GetName()))

		case err != nil:
			return results, fmt.Errorf("reading %s/%s: %w", kind, obj.GetName(), err)

		default:
			// Carry the stored resourceVersion so the update is a
			// read-modify-write against the current object rather than a
			// blind overwrite that the API server would reject.
			obj.SetResourceVersion(existing.GetResourceVersion())
			if err := c.Update(ctx, obj); err != nil {
				return results, fmt.Errorf("updating %s/%s: %w", kind, obj.GetName(), err)
			}
			results = append(results, fmt.Sprintf("%s/%s configured", kind, obj.GetName()))
		}
	}

	return results, nil
}

// PoolReport summarises what a pool ended up simulating.
type PoolReport struct {
	NodesMatched     int32
	DevicesPublished int32
}

// WaitForPool polls a pool's status until the operator reports published
// devices for the current spec, or the context is done.
//
// The observedGeneration check is what makes this correct on a re-apply.
// Without it, a pool that already had devices from the previous spec satisfies
// the wait immediately and the CLI reports the pre-update count: change
// gpusPerNode from 8 to 16 and it would still print the old total.
//
// A pool whose selector matches no simulated node reconciles successfully and
// reports zero, which looks identical to "the operator has not caught up yet".
// Returning the last observed report on timeout lets the caller tell the user
// which of the two happened rather than just failing.
func WaitForPool(ctx context.Context, c client.Client, name string, poll time.Duration) (PoolReport, error) {
	var last PoolReport

	for {
		var pool v1alpha1.GPUPool
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &pool); err != nil {
			if !apierrors.IsNotFound(err) {
				return last, err
			}
		} else {
			last = PoolReport{
				NodesMatched:     pool.Status.NodesMatched,
				DevicesPublished: pool.Status.DevicesPublished,
			}
			reconciled := pool.Status.ObservedGeneration >= pool.Generation
			if reconciled && last.DevicesPublished > 0 {
				return last, nil
			}
		}

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(poll):
		}
	}
}
