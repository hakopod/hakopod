package database

import "testing"

func TestOracleQueryCapabilitiesRequireQualifiedFreeSpec(t *testing.T) {
	free := Spec{Engine: "oracle", Version: "23.26", Mode: "standalone", Shards: 1,
		TLS: &TLSConfig{Mode: "required"}, Oracle: &OracleConfig{Edition: "free"}}
	cases := []struct {
		name      string
		change    func(*Spec)
		supported bool
	}{
		{"free", func(*Spec) {}, true},
		{"enterprise_19", func(s *Spec) { s.Version = "19"; s.Oracle.Edition = "enterprise" }, false},
		{"enterprise_23", func(s *Spec) { s.Oracle.Edition = "enterprise" }, false},
		{"invalid_version", func(s *Spec) { s.Version = "23.25" }, false},
		{"custom_image", func(s *Spec) { s.Oracle.Image = OracleFreeImage }, false},
		{"custom_registry", func(s *Spec) { s.Oracle.RegistryCredential = "private" }, false},
		{"license_declaration", func(s *Spec) { s.Oracle.LicenseConfirmed = true }, false},
		{"cluster", func(s *Spec) { s.Mode = "cluster" }, false},
		{"missing_edition", func(s *Spec) { s.Oracle = nil }, false},
		{"missing_tls", func(s *Spec) { s.TLS = nil }, false},
		{"multiple_shards", func(s *Spec) { s.Shards = 2 }, false},
		{"standalone_replicas", func(s *Spec) { s.Replicas = 1 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := free
			oracle := *free.Oracle
			spec.Oracle = &oracle
			tc.change(&spec)
			c := CapabilitiesForQuerySpec(spec)
			if c.Supported != tc.supported || c.ReadOnlySupported || c.TransactionalDML || c.TransactionalDDL ||
				c.ReadOnlyEnforcement != "unsupported" || c.TransactionScope != "none" ||
				len(c.ExecutionModes) != 1 || c.ExecutionModes[0] != "nontransactional" || c.ParameterStyle != ":1" {
				t.Fatal("Oracle capability exceeded qualified scope")
			}
		})
	}
	if CapabilitiesForQuery("oracle").Supported {
		t.Fatal("engine-only Oracle discovery must remain unsupported")
	}
}
