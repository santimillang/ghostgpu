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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// recordingReader counts reads so the wrapper can be shown to delegate rather
// than to quietly swallow them.
type recordingReader struct {
	gets, lists int
}

func (r *recordingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	r.gets++
	return nil
}

func (r *recordingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	r.lists++
	return nil
}

// TestReadOnlyRejectsWrites is the guarantee `ghostgpu capture` rests on.
//
// Capture is pointed at production on purpose, so "it does not write" has to be
// a property of the type rather than a property of the current call sites. If
// ReadOnly ever satisfies client.Writer — most likely because someone changed
// the field to an embedded client.Client — a write becomes one type assertion
// away and this fails.
func TestReadOnlyRejectsWrites(t *testing.T) {
	var wrapped any = NewReadOnly(&recordingReader{})

	if _, ok := wrapped.(client.Writer); ok {
		t.Error("ReadOnly satisfies client.Writer; capture could write to the cluster it was pointed at")
	}
	if _, ok := wrapped.(client.Client); ok {
		t.Error("ReadOnly satisfies client.Client, which includes every write method")
	}
	if _, ok := wrapped.(client.Reader); !ok {
		t.Error("ReadOnly does not satisfy client.Reader, so it cannot be used to read either")
	}
}

func TestReadOnlyDelegatesReads(t *testing.T) {
	underlying := &recordingReader{}
	reader := NewReadOnly(underlying)
	ctx := context.Background()

	if err := reader.Get(ctx, client.ObjectKey{Name: "n"}, &corev1.Node{}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := reader.List(ctx, &corev1.NodeList{}); err != nil {
		t.Fatalf("List: %v", err)
	}

	if underlying.gets != 1 || underlying.lists != 1 {
		t.Errorf("delegated %d gets and %d lists, want 1 of each", underlying.gets, underlying.lists)
	}
}
