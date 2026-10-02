//go:build !hakopod_native_acceptance || !linux

package api

import "net/http"

// Shipping servers do not register development provider probes.
func (s *Server) registerNativeProbeRoutes(_ *http.ServeMux) {}
