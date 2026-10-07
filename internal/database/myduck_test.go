package database

import "testing"

func TestMyDuckRequiresFixedStandaloneTLSShape(t *testing.T) {
	if MyDuckSourceRevision != "6e3427591fd8895df9585969e7256f958fb639bb" {
		t.Fatal("MyDuck source pin changed without updating its reviewed contract")
	}
	valid := Spec{Engine: "duckdb", Version: MyDuckVersion, Mode: "standalone", Shards: 1, TLS: &TLSConfig{Mode: "required"}}
	if err := valid.ValidateMyDuck(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Spec){
		"version":  func(s *Spec) { s.Version = "latest" },
		"mode":     func(s *Spec) { s.Mode = "cluster" },
		"shards":   func(s *Spec) { s.Shards = 2 },
		"replicas": func(s *Spec) { s.Replicas = 1 },
		"tls":      func(s *Spec) { s.TLS = nil },
		"pooling": func(s *Spec) {
			s.Pooling = &Pooling{Mode: "session", Instances: 1, MaxClientConnections: 20, DefaultPoolSize: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			change(&candidate)
			if err := candidate.ValidateMyDuck(); err == nil {
				t.Fatal("unsupported MyDuck shape was accepted")
			}
		})
	}
}
