package cluster

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
)

func TestRecoveryMutationFencesRejectQualificationDrift(t *testing.T) {
	changed := errors.New("operator storage identity changed")
	op := platformbackup.Operation{ID: "recovery-operation", Lease: "current-lease"}
	ctx := platformbackup.WithRecoveryOperation(context.Background(), op)
	check := func(_ context.Context, got platformbackup.Operation) error {
		if got.ID != op.ID || got.Lease != op.Lease {
			t.Fatal("qualification lost operation identity")
		}
		return changed
	}
	supabase := &SupabaseRecoveryRuntime{ValidateQualification: check}
	neon := &NeonRecoveryRuntime{ValidateQualification: check}
	for name, fence := range map[string]func() error{
		"supabase":       func() error { return supabase.fence(ctx) },
		"neon":           func() error { return neon.fence(ctx, op) },
		"neon-lifecycle": func() error { return (neonRecoveryLifecycle{runtime: neon, recovery: op}).Heartbeat(ctx) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := fence(); !errors.Is(err, changed) {
				t.Fatalf("mutation reached the store after drift: %v", err)
			}
		})
	}
}

func TestSupabaseRecoveryPreservesPoolerAdministrativeAuthority(t *testing.T) {
	source := managedplatform.Spec{Secrets: map[string]managedplatform.SecretReference{}}
	for _, key := range supabaseRecoveryCryptographicSecretKeys {
		source.Secrets[key] = managedplatform.SecretReference{Name: "supabase-" + key, Revision: 1}
	}
	target := source
	target.Secrets = maps.Clone(source.Secrets)
	if err := validateSupabaseRecoverySecrets(target, source); err != nil {
		t.Fatal(err)
	}
	target.Secrets["pooler-api-jwt-secret"] = managedplatform.SecretReference{Name: "supabase-pooler-api-jwt-secret", Revision: 2}
	if err := validateSupabaseRecoverySecrets(target, source); err == nil {
		t.Fatal("Supabase recovery accepted a different pooler administrative JWT revision")
	}
}

func recoveryTar(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for _, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSanitizeRecoveryTarAcceptsOnlyBoundedRegularTree(t *testing.T) {
	body := recoveryTar(t, []*tar.Header{{Name: "dir", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "dir/file", Typeflag: tar.TypeReg, Mode: 0644, Size: 3}})
	file, err := sanitizeRecoveryTar(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	reader := tar.NewReader(file)
	for _, want := range []string{"dir", "dir/file"} {
		header, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != want {
			t.Fatalf("got %q want %q", header.Name, want)
		}
		if header.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != "xxx" {
				t.Fatalf("regular content changed: %q %v", data, err)
			}
		}
	}
	if _, err = reader.Next(); err != io.EOF {
		t.Fatalf("expected end, got %v", err)
	}
}

func TestSanitizeRecoveryTarRejectsUnsafeEntries(t *testing.T) {
	cases := map[string][]*tar.Header{"traversal": {{Name: "../escape", Typeflag: tar.TypeReg}}, "absolute": {{Name: "/escape", Typeflag: tar.TypeReg}}, "symlink": {{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}}, "hardlink": {{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}}, "device": {{Name: "dev", Typeflag: tar.TypeChar}}, "duplicate": {{Name: "same", Typeflag: tar.TypeReg}, {Name: "same", Typeflag: tar.TypeReg}}}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			body := recoveryTar(t, headers)
			if file, err := sanitizeRecoveryTar(bytes.NewReader(body), int64(len(body))); err == nil {
				file.Close()
				os.Remove(file.Name())
				t.Fatal("unsafe tar accepted")
			}
		})
	}
}
