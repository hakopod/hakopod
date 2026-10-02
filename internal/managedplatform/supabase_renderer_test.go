package managedplatform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func rendererFixture() SupabaseRenderInput {
	spec := supabaseCandidateSpec()
	images := map[string]string{}
	for _, name := range SupabaseComponentNames() {
		images[name] = "registry.example.test/supabase/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	assets, err := PinnedSupabaseAssets()
	if err != nil {
		panic(err)
	}
	identities := map[string]RuntimeIdentity{}
	for _, name := range SupabaseComponentNames() {
		identities[name] = RuntimeIdentity{UID: 10000, GID: 10000}
	}
	identities["database"] = RuntimeIdentity{UID: 100, GID: 101}
	return SupabaseRenderInput{Spec: spec, PlatformID: strings.Repeat("a", 32), Images: images, Assets: assets, Identities: identities, Revision: 1, NamespaceUID: types.UID("namespace-uid"), DatabaseClaim: ObservedClaimState{Observed: true}, ApprovedEncryptedStorageClass: "encrypted-rwo", SharedStorageGID: 20000}
}

func TestSupabaseRendererProducesOwnedPrivateDeterministicObjects(t *testing.T) {
	in := rendererFixture()
	first, err := RenderSupabase(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderSupabase(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != len(second.Objects) || first.ExpectedUID != in.NamespaceUID || !first.TLSRequired {
		t.Fatal("renderer omitted its ownership or TLS precondition")
	}
	for i := range first.Objects {
		a, b := first.Objects[i].(metav1.Object), second.Objects[i].(metav1.Object)
		if a.GetName() != b.GetName() || len(a.GetOwnerReferences()) != 1 || a.GetOwnerReferences()[0].UID != in.NamespaceUID || a.GetOwnerReferences()[0].Kind != "Namespace" {
			t.Fatal("rendered order or ownership is unstable")
		}
		if service, ok := first.Objects[i].(*corev1.Service); ok && service.Spec.Type != corev1.ServiceTypeClusterIP {
			t.Fatal("renderer exposed a public service")
		}
		if deployment, ok := first.Objects[i].(*appsv1.Deployment); ok && (deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || deployment.Spec.Strategy.RollingUpdate != nil) {
			t.Fatalf("managed platform deployment %s can overlap old and new pods", deployment.Name)
		}
		if stateful, ok := first.Objects[i].(*appsv1.StatefulSet); ok && stateful.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
			t.Fatalf("managed platform StatefulSet %s does not replace one stable ordinal at a time", stateful.Name)
		}
	}
}

func TestSupabaseRendererPersistsDatabaseEncryptionAndUsesImmutableSecrets(t *testing.T) {
	in := rendererFixture()
	manifests, err := RenderSupabase(in)
	if err != nil {
		t.Fatal(err)
	}
	foundEncryption := false
	foundPrivateKeyStore := false
	foundProjectedKey := false
	foundDatabase := false
	for _, object := range manifests.Objects {
		if pvc, ok := object.(*corev1.PersistentVolumeClaim); ok && pvc.Name == "supabase-database-encryption" {
			foundEncryption = true
		}
		stateful, ok := object.(*appsv1.StatefulSet)
		if !ok {
			continue
		}
		foundDatabase = true
		container := stateful.Spec.Template.Spec.Containers[0]
		for _, mount := range container.VolumeMounts {
			if mount.Name == "database-encryption" && mount.MountPath == "/var/lib/postgresql/pgsodium-volume" && mount.SubPath == "" {
				foundPrivateKeyStore = true
			}
			if mount.Name == "database-encryption" && mount.MountPath == "/etc/postgresql-custom/pgsodium_root.key" && mount.SubPath == "keyring/pgsodium_root.key" && mount.ReadOnly {
				foundProjectedKey = true
			}
			if mount.Name == "database-encryption" && mount.MountPath == "/etc/postgresql-custom" {
				t.Fatal("database encryption claim masks native image configuration")
			}
		}
		for _, env := range container.Env {
			if env.Name == "HAKOPOD_DATABASE_DATABASE_ROLE_BOOTSTRAP" {
				t.Fatal("role bootstrap secret body was exposed through the environment")
			}
		}
	}
	if !foundEncryption || !foundPrivateKeyStore || !foundProjectedKey || !foundDatabase {
		t.Fatal("database or persistent pgsodium key storage is missing")
	}
	for _, name := range manifests.RequiredSecrets {
		if !strings.HasSuffix(name, "-r1") {
			t.Fatalf("secret %q is not an immutable revision snapshot", name)
		}
	}
}

func TestSupabaseRendererRefusesIncompleteUpstreamAssetsAndUnobservedNamespace(t *testing.T) {
	in := rendererFixture()
	delete(in.Assets, "db/roles.sql")
	if _, err := RenderSupabase(in); err == nil {
		t.Fatal("incomplete pinned upstream inventory was accepted")
	}
	in = rendererFixture()
	in.NamespaceUID = ""
	if _, err := RenderSupabase(in); err == nil {
		t.Fatal("renderer accepted an unobserved namespace")
	}
}

func TestSupabaseRendererWiresUpstreamServiceNamesAndStableSelectors(t *testing.T) {
	in := rendererFixture()
	manifests, err := RenderSupabase(in)
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]bool{}
	for _, object := range manifests.Objects {
		if service, ok := object.(*corev1.Service); ok {
			services[service.Name] = true
			if service.Name == "api-gw" && (len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 8443) {
				t.Fatal("Supabase gateway does not expose only TLS port 8443")
			}
		}
		deployment, ok := object.(*appsv1.Deployment)
		if !ok {
			continue
		}
		if _, found := deployment.Spec.Selector.MatchLabels["hakopod.io/revision"]; found {
			t.Fatal("immutable deployment selector contains a revision")
		}
		if deployment.Name == "supabase-api-gateway" {
			container := deployment.Spec.Template.Spec.Containers[0]
			for _, env := range container.Env {
				if env.Name == "GATEWAY_TLS_CERTIFICATE" {
					t.Fatal("gateway TLS certificate was exposed through the environment")
				}
			}
			if len(container.Command) != 1 || container.Command[0] != "envoy" {
				t.Fatal("Envoy still uses the upstream interpolation entrypoint")
			}
			paths := map[string]bool{}
			for _, mount := range container.VolumeMounts {
				paths[mount.MountPath] = true
			}
			for _, path := range []string{"/etc/envoy/envoy.yaml", "/etc/envoy/cds.yaml", "/etc/envoy/lds.yaml", "/etc/envoy/tls"} {
				if !paths[path] {
					t.Fatalf("Envoy mount %s is missing", path)
				}
			}
		}
	}
	for _, name := range []string{"api-gw", "auth", "db", "functions", "imgproxy", "meta", "realtime", "rest", "storage", "studio", "supavisor"} {
		if !services[name] {
			t.Fatalf("upstream service DNS name %s is missing", name)
		}
	}
}

func TestSupabaseRendererRequiresQualifiedImageIdentities(t *testing.T) {
	in := rendererFixture()
	delete(in.Identities, "database")
	if _, err := RenderSupabase(in); err == nil {
		t.Fatal("renderer guessed a missing image runtime identity")
	}
}

func TestSupabaseRendererWiresRequiredRuntimeConfiguration(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	containers := map[string]corev1.Container{}
	for _, object := range manifests.Objects {
		if deployment, ok := object.(*appsv1.Deployment); ok {
			containers[deployment.Spec.Template.Spec.Containers[0].Name] = deployment.Spec.Template.Spec.Containers[0]
		}
		if stateful, ok := object.(*appsv1.StatefulSet); ok {
			containers[stateful.Spec.Template.Spec.Containers[0].Name] = stateful.Spec.Template.Spec.Containers[0]
		}
	}
	for _, component := range []string{"edge-runtime", "studio"} {
		foundCA := false
		for _, mount := range containers[component].VolumeMounts {
			if mount.Name == "gateway-tls" {
				t.Fatalf("%s received the gateway private-key volume", component)
			}
			if mount.Name == "gateway-ca" && mount.ReadOnly && mount.MountPath == "/etc/hakopod-gateway-ca" {
				foundCA = true
			}
		}
		if !foundCA {
			t.Fatalf("%s did not receive the read-only gateway CA", component)
		}
	}
	expect := map[string]map[string]string{
		"auth":         {"GOTRUE_API_HOST": "0.0.0.0", "API_EXTERNAL_URL": "https://data.example.test/auth/v1", "GOTRUE_SITE_URL": "https://app.example.test"},
		"rest":         {"PGRST_DB_SCHEMAS": "public,storage,graphql_public", "PGRST_DB_MAX_ROWS": "1000"},
		"realtime":     {"DB_HOST": "db", "DB_PORT": "5432", "DB_USER": "hakopod_realtime"},
		"storage":      {"POSTGREST_URL": "http://rest:3000", "IMGPROXY_URL": "http://imgproxy:5001", "STORAGE_PUBLIC_URL": "https://data.example.test"},
		"edge-runtime": {"SUPABASE_URL": "https://api-gw:8443", "VERIFY_JWT": "true"},
	}
	for component, wanted := range expect {
		env := map[string]string{}
		for _, item := range containers[component].Env {
			env[item.Name] = item.Value
		}
		for name, value := range wanted {
			if env[name] != value {
				t.Fatalf("%s %s wiring mismatch: %q", component, name, env[name])
			}
		}
	}
	if got := containers["edge-runtime"].Args; len(got) != 5 || got[4] != "/home/deno/functions/main" {
		t.Fatal("Edge Runtime command is incomplete")
	}
	realtime := containers["realtime"]
	if len(realtime.Command) != 2 || realtime.Command[0] != "/bin/sh" || len(realtime.Args) != 1 || !strings.Contains(realtime.Args[0], "/app/bin/migrate") || !strings.Contains(realtime.Args[0], "exec /app/bin/server") || strings.Contains(realtime.Args[0], "sudo") || strings.Contains(realtime.Args[0], "set -x") || strings.Contains(realtime.Args[0], "/app/run.sh") {
		t.Fatal("Realtime did not bypass the privileged or credential-logging entrypoint")
	}
	if got := containers["database"].Args; len(got) != 19 || got[0] != "postgres" || got[4] != "data_directory=/var/lib/postgresql/data/pgdata" {
		t.Fatal("PostgreSQL command is incomplete")
	}
	if got := containers["pooler"].Command; len(got) != 2 || got[0] != "/bin/sh" {
		t.Fatal("Supavisor startup command is incomplete")
	}
}

func TestSupabaseRendererEnforcesSharedVolumeAndStorageContracts(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		if pvc, ok := object.(*corev1.PersistentVolumeClaim); ok && (pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "encrypted-rwo") {
			t.Fatalf("PVC %s lacks the approved encrypted StorageClass", pvc.Name)
		}
		deployment, ok := object.(*appsv1.Deployment)
		if !ok {
			continue
		}
		pod := deployment.Spec.Template.Spec
		if pod.NodeSelector["kubernetes.io/hostname"] != "worker-a" {
			t.Fatalf("%s is not pinned with the shared RWO claims", deployment.Name)
		}
		if pod.NodeSelector["kubernetes.io/arch"] != "amd64" {
			t.Fatalf("%s does not select the qualified amd64 architecture", deployment.Name)
		}
		if pod.NodeSelector["kubernetes.io/os"] != "linux" {
			t.Fatalf("%s does not select the qualified Linux operating system", deployment.Name)
		}
		if deployment.Name == "supabase-studio" {
			for _, mount := range pod.Containers[0].VolumeMounts {
				if mount.Name == "edge-functions" && !mount.ReadOnly {
					t.Fatal("Studio can write the Edge Functions claim")
				}
			}
		}
		if deployment.Name == "supabase-edge-runtime" {
			env := map[string]string{}
			for _, item := range pod.Containers[0].Env {
				env[item.Name] = item.Value
			}
			if env["DENO_DIR"] != "/var/cache/deno" {
				t.Fatal("Edge Runtime cache is not in its bounded writable volume")
			}
		}
	}
}

func TestSupabaseDatabasePreservesImageBootstrapLayout(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	var pod *corev1.PodSpec
	for _, object := range manifests.Objects {
		if stateful, ok := object.(*appsv1.StatefulSet); ok && stateful.Name == "supabase-database" {
			pod = &stateful.Spec.Template.Spec
		}
	}
	if pod == nil || len(pod.Containers) != 1 {
		t.Fatal("database StatefulSet is missing")
	}
	container := pod.Containers[0]
	if len(container.Command) != 0 || len(container.Args) != 19 || container.Args[0] != "postgres" {
		t.Fatal("database no longer uses the image entrypoint with its postgres argument")
	}
	if strings.Join(container.Args, "\x00") != strings.Join([]string{"postgres", "-c", "config_file=/etc/postgresql/postgresql.conf", "-c", "data_directory=/var/lib/postgresql/data/pgdata", "-c", "hba_file=/etc/postgresql-custom/hakopod-pg_hba.conf", "-c", "ssl=on", "-c", "ssl_min_protocol_version=TLSv1.2", "-c", "ssl_cert_file=/etc/postgresql-tls/current/tls.crt", "-c", "ssl_key_file=/etc/postgresql-tls/current/tls.key", "-c", "ssl_ca_file=/etc/postgresql-tls/current/ca.crt", "-c", "log_min_messages=fatal"}, "\x00") {
		t.Fatal("database does not enforce its owned data directory and TLS-only transport")
	}
	environment := map[string]string{}
	for _, item := range container.Env {
		environment[item.Name] = item.Value
	}
	if environment["PGDATA"] != "/var/lib/postgresql/data/pgdata" {
		t.Fatal("database PGDATA does not use the UID-owned child directory")
	}
	want := map[string]bool{
		"/docker-entrypoint-initdb.d/migrations/97-_supabase.sql":                  false,
		"/docker-entrypoint-initdb.d/migrations/99-logs.sql":                       false,
		"/docker-entrypoint-initdb.d/migrations/99-pooler.sql":                     false,
		"/docker-entrypoint-initdb.d/migrations/99-realtime.sql":                   false,
		"/docker-entrypoint-initdb.d/init-scripts/98-webhooks.sql":                 false,
		"/docker-entrypoint-initdb.d/init-scripts/99-jwt.sql":                      false,
		"/docker-entrypoint-initdb.d/init-scripts/99-z-hakopod-role-passwords.sql": false,
	}
	for _, mount := range container.VolumeMounts {
		if mount.MountPath == "/docker-entrypoint-initdb.d" {
			t.Fatal("database assets hide the image bootstrap launcher")
		}
		if _, ok := want[mount.MountPath]; ok {
			if mount.SubPath == "" || !mount.ReadOnly {
				t.Fatalf("bootstrap asset %s is not a read-only single-file projection", mount.MountPath)
			}
			want[mount.MountPath] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("bootstrap asset %s is missing", path)
		}
	}
	postgresRunBounded := false
	for _, volume := range pod.Volumes {
		if volume.Name == "postgres-run" {
			postgresRunBounded = volume.EmptyDir != nil && volume.EmptyDir.SizeLimit != nil && !volume.EmptyDir.SizeLimit.IsZero()
		}
	}
	if !postgresRunBounded {
		t.Fatal("postgres runtime directory is missing or unbounded")
	}
	if len(pod.InitContainers) != 2 {
		t.Fatal("database must initialize its persisted pgsodium key and restricted TLS files")
	}
	init := pod.InitContainers[0]
	if init.Name != "initialize-pgsodium-key" || len(init.Command) != 2 || init.Command[0] != "/bin/sh" || init.SecurityContext == nil || init.SecurityContext.RunAsNonRoot == nil || !*init.SecurityContext.RunAsNonRoot || init.SecurityContext.AllowPrivilegeEscalation == nil || *init.SecurityContext.AllowPrivilegeEscalation || init.SecurityContext.ReadOnlyRootFilesystem == nil || !*init.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("pgsodium key initializer is not the bounded qualified non-root container")
	}
	if len(init.Args) != 5 || strings.Contains(init.Args[0], "echo") || strings.Contains(init.Args[0], "chmod 0600 \"$key\"") || !strings.Contains(init.Args[0], "umask 077") || !strings.Contains(init.Args[0], "chmod 0700 \"$directory\"") || !strings.Contains(init.Args[0], "test ! -L") || !strings.Contains(init.Args[0], "ln \"$temporary\" \"$key\"") || init.Args[2] != "/var/lib/postgresql/pgsodium-volume/keyring" || init.Args[3] != strconv.FormatInt(*init.SecurityContext.RunAsUser, 10) || init.Args[4] != strconv.FormatInt(*init.SecurityContext.RunAsGroup, 10) {
		t.Fatal("pgsodium key initializer does not enforce the private key contract")
	}
	keyVolumeMountedAtParent := false
	for _, mount := range init.VolumeMounts {
		if mount.Name == "database-encryption" && mount.MountPath == "/var/lib/postgresql/pgsodium-volume" && mount.SubPath == "" && !mount.ReadOnly {
			keyVolumeMountedAtParent = true
		}
	}
	if !keyVolumeMountedAtParent {
		t.Fatal("pgsodium initializer does not mount the PVC parent of its private keyring path")
	}
	tlsInit := pod.InitContainers[1]
	if tlsInit.Name != "initialize-database-tls" || tlsInit.SecurityContext == nil || tlsInit.SecurityContext.RunAsUser == nil || *tlsInit.SecurityContext.RunAsUser != 100 || !strings.Contains(tlsInit.Args[0], "chmod 0600 \"$stage/tls.key\"") || !strings.Contains(tlsInit.Args[0], "mv \"$stage\" \"$current\"") {
		t.Fatal("database TLS key is not copied to a qualified-user mode-0600 runtime volume")
	}
	if supabasePostgresHBA != "local all all trust\nhostnossl all all 0.0.0.0/0 reject\nhostnossl all all ::/0 reject\nhostssl all all 0.0.0.0/0 scram-sha-256\nhostssl all all ::/0 scram-sha-256\n" {
		t.Fatal("database host authentication does not reject plaintext before TLS password authentication")
	}
	if pod.SecurityContext == nil || pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != *init.SecurityContext.RunAsGroup || len(pod.SecurityContext.SupplementalGroups) != 1 || pod.SecurityContext.SupplementalGroups[0] != *init.SecurityContext.RunAsGroup {
		t.Fatal("database-only persistent volumes do not use the qualified database group")
	}
}

func renderedSupabaseKeyInitializer(t *testing.T) corev1.Container {
	t.Helper()
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		if stateful, ok := object.(*appsv1.StatefulSet); ok && stateful.Name == "supabase-database" {
			return stateful.Spec.Template.Spec.InitContainers[0]
		}
	}
	t.Fatal("database key initializer is missing")
	return corev1.Container{}
}

func runSupabaseKeyInitializer(init corev1.Container, directory string) error {
	args := append(append([]string{}, init.Command[1:]...), init.Args...)
	args[3] = directory
	args[4] = strconv.Itoa(os.Getuid())
	args[5] = strconv.Itoa(os.Getgid())
	return exec.Command(init.Command[0], args...).Run()
}

func renderedSupabaseTLSInitializer(t *testing.T) corev1.Container {
	t.Helper()
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		if stateful, ok := object.(*appsv1.StatefulSet); ok && stateful.Name == "supabase-database" {
			return stateful.Spec.Template.Spec.InitContainers[1]
		}
	}
	t.Fatal("database TLS initializer is missing")
	return corev1.Container{}
}

func runSupabaseTLSInitializer(init corev1.Container, source, target string) error {
	script := strings.ReplaceAll(init.Args[0], "/source", source)
	script = strings.ReplaceAll(script, "/target", target)
	return exec.Command(init.Command[0], init.Command[1], script).Run()
}

func TestSupabaseDatabaseTLSInitializerUnderstandsProjectedSecrets(t *testing.T) {
	init := renderedSupabaseTLSInitializer(t)
	files := map[string]string{"tls.crt": "certificate", "tls.key": "private-key", "ca.crt": "certificate-authority"}
	order := []string{"tls.crt", "tls.key", "ca.crt"}
	for interruptedAt := range order {
		interruptedAt := interruptedAt
		t.Run("recovers interrupted copy "+order[interruptedAt], func(t *testing.T) {
			root := t.TempDir()
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			stamp := "..2026_10_01_12_34_56.1234567890"
			data := filepath.Join(source, stamp)
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			for name, value := range files {
				if err := os.WriteFile(filepath.Join(data, name), []byte(value), 0440); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join("..data", name), filepath.Join(source, name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(stamp, filepath.Join(source, "..data")); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(target, ".database-tls-staging")
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			for index := 0; index <= interruptedAt; index++ {
				value := []byte(files[order[index]])
				if index == interruptedAt {
					value = nil
				}
				if err := os.WriteFile(filepath.Join(stage, order[index]), value, 0400); err != nil {
					t.Fatal(err)
				}
			}
			if err := runSupabaseTLSInitializer(init, source, target); err != nil {
				t.Fatal(err)
			}
			for name, value := range files {
				got, err := os.ReadFile(filepath.Join(target, "current", name))
				if err != nil || string(got) != value {
					t.Fatalf("copied %s does not match the projected secret", name)
				}
				info, err := os.Lstat(filepath.Join(target, "current", name))
				wantMode := os.FileMode(0644)
				if name == "tls.key" {
					wantMode = 0600
				}
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != wantMode {
					t.Fatalf("copied %s has an unsafe type or mode", name)
				}
			}
			if err := runSupabaseTLSInitializer(init, source, target); err != nil {
				t.Fatal("completed publication is not restart safe:", err)
			}
		})
	}
	t.Run("rejects traversal target", func(t *testing.T) {
		badSource, badTarget := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(badSource, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(badTarget, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../outside", filepath.Join(badSource, "..data")); err != nil {
			t.Fatal(err)
		}
		if err := runSupabaseTLSInitializer(init, badSource, badTarget); err == nil {
			t.Fatal("projected-secret traversal target was accepted")
		}
	})
}

func TestSupabaseDatabaseKeyInitializerExecutesSafely(t *testing.T) {
	init := renderedSupabaseKeyInitializer(t)
	t.Run("fresh and restart", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "keyring")
		if err := runSupabaseKeyInitializer(init, directory); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(directory, "pgsodium_root.key")
		first, err := os.ReadFile(key)
		if err != nil || len(first) != 64 {
			t.Fatal("fresh key is missing or malformed")
		}
		info, err := os.Lstat(key)
		if err != nil || info.Mode().Perm() != 0600 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("fresh key permissions or file type are unsafe")
		}
		if err = runSupabaseKeyInitializer(init, directory); err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(key)
		if string(first) != string(second) {
			t.Fatal("restart replaced persisted key material")
		}
	})
	t.Run("concurrent publication", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "keyring")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		var wait sync.WaitGroup
		errors := make(chan error, 8)
		for range 8 {
			wait.Add(1)
			go func() { defer wait.Done(); errors <- runSupabaseKeyInitializer(init, directory) }()
		}
		wait.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatal(err)
			}
		}
		value, err := os.ReadFile(filepath.Join(directory, "pgsodium_root.key"))
		if err != nil || len(value) != 64 {
			t.Fatal("concurrent initialization did not publish one complete key")
		}
	})
	t.Run("rejects malformed regular file", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "keyring")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(directory, "pgsodium_root.key")
		if err := os.WriteFile(key, []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runSupabaseKeyInitializer(init, directory); err == nil {
			t.Fatal("malformed existing key was accepted")
		}
		value, _ := os.ReadFile(key)
		if string(value) != "invalid" {
			t.Fatal("malformed existing key was replaced")
		}
	})
	t.Run("rejects symlink", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "keyring")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(directory, "target")
		if err := os.WriteFile(target, []byte(strings.Repeat("a", 64)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(directory, "pgsodium_root.key")); err != nil {
			t.Fatal(err)
		}
		if err := runSupabaseKeyInitializer(init, directory); err == nil {
			t.Fatal("symlink key was accepted")
		}
	})
	t.Run("rejects and preserves wrong mode", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "keyring")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(directory, "pgsodium_root.key")
		if err := os.WriteFile(key, []byte(strings.Repeat("b", 64)), 0640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(key, 0640); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(key)
		if err != nil {
			t.Fatal(err)
		}
		if before.Mode().Perm() != 0640 {
			t.Fatalf("wrong-mode fixture has mode %04o", before.Mode().Perm())
		}
		if err := runSupabaseKeyInitializer(init, directory); err == nil {
			t.Fatal("wrong-mode key was accepted")
		}
		info, err := os.Stat(key)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0640 {
			t.Fatal("wrong-mode key permissions were silently changed")
		}
	})
	t.Run("rejects wrong identity", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "keyring")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(directory, "pgsodium_root.key")
		if err := os.WriteFile(key, []byte(strings.Repeat("c", 64)), 0600); err != nil {
			t.Fatal(err)
		}
		args := append(append([]string{}, init.Command[1:]...), init.Args...)
		args[3] = directory
		args[4] = strconv.Itoa(os.Getuid() + 1)
		args[5] = strconv.Itoa(os.Getgid())
		if err := exec.Command(init.Command[0], args...).Run(); err == nil {
			t.Fatal("wrong-owner key was accepted")
		}
	})
}

func TestSupabasePoolerUsesBoundedTemporaryHomes(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		deployment, ok := object.(*appsv1.Deployment)
		if !ok || deployment.Name != "supabase-pooler" {
			continue
		}
		container := deployment.Spec.Template.Spec.Containers[0]
		if len(container.Command) != 2 || container.Command[0] != "/bin/sh" || container.Command[1] != "-ceu" || len(container.Args) != 1 {
			t.Fatal("pooler direct command is missing")
		}
		for _, required := range []string{"HOME=/tmp/pooler-home", "MIX_HOME=/tmp/pooler-mix", "HEX_HOME=/tmp/pooler-hex", "/app/bin/migrate", "exec /app/bin/server"} {
			if !strings.Contains(container.Args[0], required) {
				t.Fatalf("pooler startup lacks %s", required)
			}
		}
		if strings.Contains(container.Args[0], "sudo") || strings.Contains(container.Args[0], "/app/run.sh") {
			t.Fatal("pooler uses the upstream privileged wrapper")
		}
		return
	}
	t.Fatal("pooler Deployment is missing")
}

func TestSupabasePoolerUsesSeparateAdministrativeJWT(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		deployment, ok := object.(*appsv1.Deployment)
		if !ok || deployment.Name != "supabase-pooler" {
			continue
		}
		env := map[string]corev1.EnvVar{}
		for _, item := range deployment.Spec.Template.Spec.Containers[0].Env {
			env[item.Name] = item
		}
		api := env["API_JWT_SECRET"].ValueFrom
		metrics := env["METRICS_JWT_SECRET"].ValueFrom
		if api == nil || api.SecretKeyRef == nil || api.SecretKeyRef.Name != "supabase-pooler-api-jwt-secret-r1" || api.SecretKeyRef.Key != "value" {
			t.Fatal("pooler admin API does not use its dedicated immutable JWT secret")
		}
		if metrics == nil || metrics.SecretKeyRef == nil || metrics.SecretKeyRef.Name != "supabase-jwt-secret-r1" || metrics.SecretKeyRef.Key != "value" {
			t.Fatal("pooler metrics no longer use the platform JWT secret")
		}
		if api.SecretKeyRef.Name == metrics.SecretKeyRef.Name {
			t.Fatal("pooler admin and application JWT authority are shared")
		}
		return
	}
	t.Fatal("pooler Deployment is missing")
}

func TestSupabaseRendererWiresDatabaseTLSWithoutPrivateKeyDisclosure(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		var pod *corev1.PodSpec
		if deployment, ok := object.(*appsv1.Deployment); ok {
			pod = &deployment.Spec.Template.Spec
		}
		if stateful, ok := object.(*appsv1.StatefulSet); ok {
			pod = &stateful.Spec.Template.Spec
		}
		if pod == nil {
			continue
		}
		container := pod.Containers[0]
		if container.Name == "database" {
			volumes := map[string]corev1.Volume{}
			for _, volume := range pod.Volumes {
				volumes[volume.Name] = volume
			}
			source, runtime := volumes["database-tls-source"], volumes["database-tls-runtime"]
			if source.Secret == nil || source.Secret.SecretName != "supabase-database-tls-certificate-r1" || len(source.Secret.Items) != 3 {
				t.Fatal("database TLS source is not the immutable three-key projection")
			}
			if runtime.EmptyDir == nil || runtime.EmptyDir.SizeLimit == nil || runtime.EmptyDir.SizeLimit.IsZero() {
				t.Fatal("database TLS runtime volume is missing or unbounded")
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == "database-tls-source" {
					t.Fatal("PostgreSQL received the projected private-key secret")
				}
			}
			continue
		}
		needsCA := map[string]bool{"auth": true, "pooler": true, "postgres-meta": true, "realtime": true, "rest": true, "storage": true}
		if !needsCA[container.Name] {
			continue
		}
		found := false
		for _, mount := range container.VolumeMounts {
			if mount.Name == "database-ca" && mount.MountPath == "/etc/hakopod-database-ca" && mount.ReadOnly {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s lacks the read-only database CA", container.Name)
		}
		env := map[string]corev1.EnvVar{}
		for _, item := range container.Env {
			env[item.Name] = item
		}
		if container.Name == "realtime" && (env["DB_SSL"].Value != "true" || env["DB_SSL_CA_CERT"].Value != "/etc/hakopod-database-ca/ca.crt") {
			t.Fatal("Realtime TLS controls are incomplete")
		}
		if container.Name == "pooler" && (env["DATABASE_SSL_CA_CERT"].Value != "/etc/hakopod-database-ca/ca.crt" || env["DATABASE_SSL_SERVER_NAME"].Value != "db" || env["GLOBAL_UPSTREAM_CA_PATH"].Value != "/etc/hakopod-database-ca/ca.crt") {
			t.Fatal("pooler TLS controls are incomplete")
		}
		if container.Name == "postgres-meta" {
			root := env["PG_META_DB_SSL_ROOT_CERT"]
			if env["PG_META_DB_SSL_MODE"].Value != "verify-full" || root.ValueFrom == nil || root.ValueFrom.SecretKeyRef == nil || root.ValueFrom.SecretKeyRef.Key != "ca.crt" {
				t.Fatal("postgres-meta does not receive verify-full and CA PEM")
			}
		}
		if container.Name == "storage" {
			root := env["DATABASE_SSL_ROOT_CERT"]
			if root.ValueFrom == nil || root.ValueFrom.SecretKeyRef == nil || root.ValueFrom.SecretKeyRef.Key != "ca.crt" {
				t.Fatal("Storage does not receive CA PEM")
			}
		}
	}
}

func TestSupabaseRendererKeepsStudioAndEdgeRuntimeOffTheDatabaseNetwork(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, object := range manifests.Objects {
		deployment, ok := object.(*appsv1.Deployment)
		if !ok {
			continue
		}
		container := deployment.Spec.Template.Spec.Containers[0]
		if container.Name != "edge-runtime" && container.Name != "studio" {
			continue
		}
		seen[container.Name] = true
		for _, mount := range container.VolumeMounts {
			if mount.Name == "database-ca" {
				t.Fatalf("%s received a direct database CA mount", container.Name)
			}
		}
		for _, env := range container.Env {
			if env.Name == "SUPABASE_DB_URL" {
				t.Fatalf("%s received an unsupported direct database URL", container.Name)
			}
		}
	}
	if !seen["edge-runtime"] || !seen["studio"] {
		t.Fatal("edge-runtime or studio Deployment is missing")
	}
}

func TestSupabasePoolerAssetRequiresVerifiedUpstreamTLS(t *testing.T) {
	assets, err := PinnedSupabaseAssets()
	if err != nil {
		t.Fatal(err)
	}
	asset := assets["pooler/pooler.exs"]
	for _, required := range []string{
		`System.fetch_env!("DATABASE_SSL_CA_CERT")`,
		`"upstream_ssl" => true`,
		`"upstream_verify" => "peer"`,
		`"upstream_tls_ca" => upstream_tls_ca`,
		`"sni_hostname" => System.get_env("DATABASE_SSL_SERVER_NAME", "db")`,
	} {
		if !strings.Contains(asset, required) {
			t.Fatalf("pooler asset lacks %s", required)
		}
	}
}

func TestSupabaseRendererUsesAuthoritativeJWTAndHealthContracts(t *testing.T) {
	manifests, err := RenderSupabase(rendererFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		deployment, ok := object.(*appsv1.Deployment)
		if !ok {
			continue
		}
		container := deployment.Spec.Template.Spec.Containers[0]
		if container.ReadinessProbe == nil {
			t.Fatalf("%s has no readiness probe", deployment.Name)
		}
		if deployment.Name == "supabase-rest" {
			count := 0
			for _, env := range container.Env {
				if env.Name == "PGRST_JWT_SECRET" {
					count++
				}
				if env.Name == "PGRST_JWT_JWKS" {
					t.Fatal("PostgREST received an ignored JWKS variable")
				}
			}
			if count != 1 || container.ReadinessProbe.Exec == nil || container.StartupProbe.TCPSocket == nil || container.StartupProbe.FailureThreshold != 60 {
				t.Fatal("PostgREST JWT, readiness or startup wiring is incomplete")
			}
		}
	}
}

func TestSupabaseRendererRejectsUncoordinatedSecretRotation(t *testing.T) {
	in := rendererFixture()
	previous := in.Spec
	previous.Secrets = cloneMap(in.Spec.Secrets)
	previousConfig := *in.Spec.Supabase
	previous.Supabase = &previousConfig
	in.DatabaseClaim = ObservedClaimState{Observed: true, UID: types.UID("database-claim-uid")}
	in.PreviousSpec = &previous
	in.Revision = 2
	changed := in.Spec.Secrets["auth-database-url"]
	changed.Revision++
	in.Spec.Secrets["auth-database-url"] = changed
	if _, err := RenderSupabase(in); err == nil {
		t.Fatal("uncoordinated database credential rotation was accepted")
	}
	missingClass := rendererFixture()
	missingClass.ApprovedEncryptedStorageClass = ""
	if _, err := RenderSupabase(missingClass); err == nil {
		t.Fatal("unencrypted storage class was accepted")
	}
	bypass := rendererFixture()
	bypass.Revision = 7
	if _, err := RenderSupabase(bypass); err == nil {
		t.Fatal("a later revision bypassed observed durable-state checks")
	}
	unobserved := rendererFixture()
	unobserved.DatabaseClaim.Observed = false
	if _, err := RenderSupabase(unobserved); err == nil {
		t.Fatal("unobserved database state was treated as an initial provision")
	}
	changedSetting := rendererFixture()
	prior := changedSetting.Spec
	prior.Secrets = cloneMap(changedSetting.Spec.Secrets)
	priorConfig := *changedSetting.Spec.Supabase
	prior.Supabase = &priorConfig
	changedSetting.DatabaseClaim = ObservedClaimState{Observed: true, UID: "database-claim-uid"}
	changedSetting.PreviousSpec = &prior
	changedSetting.Revision = 2
	changedSetting.Spec.Supabase.JWTExpirySeconds++
	changedSettingResult, err := RenderSupabase(changedSetting)
	if err != nil {
		t.Fatal(err)
	}
	settingMarker := false
	for _, object := range changedSettingResult.Objects {
		marker, ok := object.(*corev1.ConfigMap)
		if ok && marker.Name == "supabase-database-credentials-r2" {
			settingMarker = len(marker.Data["reference_fingerprint"]) == 64
		}
	}
	if !settingMarker {
		t.Fatal("JWT expiry update has no durable database migration marker")
	}
	databaseRotation := rendererFixture()
	prior = databaseRotation.Spec
	prior.Secrets = cloneMap(databaseRotation.Spec.Secrets)
	priorConfig = *databaseRotation.Spec.Supabase
	prior.Supabase = &priorConfig
	databaseRotation.DatabaseClaim = ObservedClaimState{Observed: true, UID: "database-claim-uid"}
	databaseRotation.PreviousSpec = &prior
	databaseRotation.Revision = 2
	for _, key := range supabaseDatabaseCredentialKeys {
		ref := databaseRotation.Spec.Secrets[key]
		ref.Revision++
		databaseRotation.Spec.Secrets[key] = ref
	}
	rotated, err := RenderSupabase(databaseRotation)
	if err != nil {
		t.Fatal(err)
	}
	markerFound := false
	for _, object := range rotated.Objects {
		marker, ok := object.(*corev1.ConfigMap)
		if !ok || marker.Name != "supabase-database-credentials-r2" {
			continue
		}
		markerFound = len(marker.Data["reference_fingerprint"]) == 64 && len(marker.Data) == 1 && marker.Immutable != nil && *marker.Immutable
	}
	if !markerFound {
		t.Fatal("database credential rotation has no immutable non-secret completion marker")
	}
	unsafeKey := rendererFixture()
	prior = unsafeKey.Spec
	prior.Secrets = cloneMap(unsafeKey.Spec.Secrets)
	priorConfig = *unsafeKey.Spec.Supabase
	prior.Supabase = &priorConfig
	unsafeKey.DatabaseClaim = ObservedClaimState{Observed: true, UID: "database-claim-uid"}
	unsafeKey.PreviousSpec = &prior
	unsafeKey.Revision = 2
	ref := unsafeKey.Spec.Secrets["jwt-secret"]
	ref.Revision++
	unsafeKey.Spec.Secrets["jwt-secret"] = ref
	if _, err := RenderSupabase(unsafeKey); err == nil {
		t.Fatal("JWT secret rotation without a migration protocol was accepted")
	}
}

func TestSupabaseRendererAllowsOnlyBoundCurrentCreateDatabaseClaim(t *testing.T) {
	in := rendererFixture()
	in.DatabaseClaim = ObservedClaimState{Observed: true, UID: types.UID("database-claim-uid")}
	in.DatabaseClaimFromCurrentCreate = true
	if _, err := RenderSupabase(in); err != nil {
		t.Fatal(err)
	}

	missing := in
	missing.DatabaseClaim.UID = ""
	if _, err := RenderSupabase(missing); err == nil {
		t.Fatal("missing current-create database claim was accepted")
	}

	wrongRevision := in
	wrongRevision.Revision = 2
	if _, err := RenderSupabase(wrongRevision); err == nil {
		t.Fatal("later revision claimed current-create provisioning")
	}

	withPrevious := in
	previous := in.Spec
	withPrevious.PreviousSpec = &previous
	if _, err := RenderSupabase(withPrevious); err == nil {
		t.Fatal("update claimed current-create provisioning")
	}
}

func TestSupabaseDatabaseMigrationFingerprintCoversExpiryAndCredentialReferences(t *testing.T) {
	base := rendererFixture().Spec
	original := supabaseDatabaseCredentialFingerprint(base)
	expiry := base
	expiry.Secrets = cloneMap(base.Secrets)
	expiryConfig := *base.Supabase
	expiry.Supabase = &expiryConfig
	expiry.Supabase.JWTExpirySeconds++
	if supabaseDatabaseCredentialFingerprint(expiry) == original {
		t.Fatal("JWT expiry did not change the database migration fingerprint")
	}
	credentials := base
	credentials.Secrets = cloneMap(base.Secrets)
	ref := credentials.Secrets["auth-database-url"]
	ref.Revision++
	credentials.Secrets["auth-database-url"] = ref
	if supabaseDatabaseCredentialFingerprint(credentials) == original {
		t.Fatal("database credential reference did not change the migration fingerprint")
	}
}

func TestSupabaseRendererRejectsUnsafeExternalEgress(t *testing.T) {
	for _, cidrs := range [][]string{{"0.0.0.0/0"}, {"10.0.0.0/24"}, {"169.254.169.254/32"}, {"203.0.113.7/32", "203.0.113.7/32"}, {"203.0.113.7/24"}} {
		in := rendererFixture()
		in.ApprovedExternalHTTPSCIDRs = cidrs
		if _, err := RenderSupabase(in); err == nil {
			t.Fatalf("unsafe external egress was accepted: %v", cidrs)
		}
	}
}
