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

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadOnly exposes only the reads of a Kubernetes client.
//
// `ghostgpu capture` is the one command pointed at a cluster the user cares
// about — production, by design, since reproducing production is the point.
// ghostgpu's safety invariant keeps it off nodes kwok does not manage; this is
// that invariant's inverse and deserves the same treatment. Rather than
// promising the capture path performs no writes, this makes a write
// unreachable: the underlying client is an unexported field with no accessor,
// so there is no Create, Update, Patch, or Delete to call and no type assertion
// that recovers one.
//
// The field is deliberately not embedded. Embedding would promote whatever
// methods its type happens to have, so widening it to client.Client one day
// would quietly restore every write method. TestReadOnlyRejectsWrites pins
// that.
type ReadOnly struct {
	reader client.Reader
}

// NewReadOnly narrows a client to its reads.
func NewReadOnly(reader client.Reader) ReadOnly {
	return ReadOnly{reader: reader}
}

// Get retrieves one object.
func (r ReadOnly) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return r.reader.Get(ctx, key, obj, opts...)
}

// List retrieves a collection.
func (r ReadOnly) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.reader.List(ctx, list, opts...)
}
