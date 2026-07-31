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

package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultAddr serves on dcgm-exporter's conventional port, so an existing
// scrape config or ServiceMonitor finds ghostgpu without being rewritten.
const DefaultAddr = ":9400"

// Server exposes the simulated fleet's metrics over HTTP.
//
// It is a manager Runnable, so it starts and stops with the operator and
// inherits its shutdown signal rather than needing a lifecycle of its own.
type Server struct {
	// Addr to listen on. Empty means DefaultAddr.
	Addr string

	// Exporter produces the samples.
	Exporter *Exporter
}

// NeedLeaderElection reports false: every replica should serve metrics.
//
// Serving only from the leader would make the endpoint go silent during a
// failover, which reads as a fleet that vanished rather than an operator that
// restarted.
func (s *Server) NeedLeaderElection() bool { return false }

// Start serves until the context is done.
func (s *Server) Start(ctx context.Context) error {
	registry := prometheus.NewRegistry()
	if err := registry.Register(s.Exporter); err != nil {
		return fmt.Errorf("registering the simulated GPU collector: %w", err)
	}

	addr := s.Addr
	if addr == "" {
		addr = DefaultAddr
	}

	mux := http.NewServeMux()
	mux.Handle(Path, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log := logf.FromContext(ctx).WithName("gpu-metrics")
	log.Info("serving simulated GPU metrics", "addr", addr, "path", Path)

	errs := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
