package database

import "fmt"

// MyDuckVersion is the upstream OCI version label. MyDuckSourceRevision pins
// the immutable source reviewed for that build. Runtime support remains gated
// until this exact pair passes native qualification.
const MyDuckVersion = "0.3.1-dev.20260919.3"
const MyDuckSourceRevision = "6e3427591fd8895df9585969e7256f958fb639bb"

func (s Spec) ValidateMyDuck() error {
	if s.Version != MyDuckVersion || s.Mode != "standalone" || s.Shards != 1 || s.Replicas != 0 || !s.TLSRequired() {
		return fmt.Errorf("DuckDB (MyDuck) requires version %s, standalone mode, one instance and required TLS", MyDuckVersion)
	}
	if s.Pooling != nil {
		return fmt.Errorf("DuckDB (MyDuck) does not support managed connection pooling")
	}
	return nil
}
