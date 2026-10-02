//go:build hakopod_native_acceptance && linux

package nativeacceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type configuration struct {
	SchemaVersion int               `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Kind          string            `json:"kind"`
	Project       string            `json:"project"`
	Environment   string            `json:"environment"`
	Context       string            `json:"context"`
	ClusterUID    string            `json:"cluster_uid"`
	NodeUIDs      map[string]string `json:"node_uids"`
	Kubeconfig    string            `json:"kubeconfig"`
	SourceRoot    string            `json:"source_root"`
	SourceFiles   map[string]string `json:"source_files"`
	BinarySHA256  string            `json:"binary_sha256"`
	ExpiresAt     time.Time         `json:"expires_at"`
	guard         *runGuard
}

type fileIdentity struct {
	path       string
	maximum    int64
	restricted bool
	info       os.FileInfo
	digest     string
}

type runGuard struct {
	files []fileIdentity
	kube  kubernetes.Interface
	rest  *rest.Config
}

var active atomic.Pointer[configuration]
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func openFile(path string, maximum int64, restricted bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("acceptance file must have an absolute path")
	}
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("acceptance path is unavailable or symbolic")
		}
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, fmt.Errorf("acceptance file is unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0022 != 0 || (restricted && (info.Mode&0077 != 0 || info.Size < 1)) || (info.Uid != 0 && info.Uid != uint32(os.Geteuid())) || info.Size < 0 || info.Size > maximum {
		f.Close()
		return nil, fmt.Errorf("acceptance file must be owned, restricted and bounded")
	}
	return f, nil
}

func protected(path string, maximum int64) (*os.File, error) { return openFile(path, maximum, true) }

func readIdentity(path string, maximum int64, restricted bool) ([]byte, fileIdentity, error) {
	f, err := openFile(path, maximum, restricted)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, fileIdentity{}, fmt.Errorf("acceptance file identity is unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maximum+1))
	after, statErr := f.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || int64(len(raw)) > maximum || !sameIdentity(before, after) || !sameIdentity(after, current) {
		return nil, fileIdentity{}, fmt.Errorf("acceptance file changed during verification")
	}
	digest := sha256.Sum256(raw)
	return raw, fileIdentity{path: path, maximum: maximum, restricted: restricted, info: after, digest: hex.EncodeToString(digest[:])}, nil
}

func sameIdentity(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func (identity fileIdentity) verify() error {
	_, actual, err := readIdentity(identity.path, identity.maximum, identity.restricted)
	if err != nil || actual.digest != identity.digest || !sameIdentity(identity.info, actual.info) {
		return fmt.Errorf("acceptance file identity changed")
	}
	return nil
}

func hashFile(path string, maximum int64) (string, error) {
	f, err := openFile(path, maximum, false)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("acceptance file identity is unavailable")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maximum+1))
	if err != nil || n > maximum {
		return "", fmt.Errorf("acceptance file exceeded its bound")
	}
	after, err := f.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return "", fmt.Errorf("acceptance file changed during verification")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validate(c configuration, now time.Time) error {
	if c.SchemaVersion != 1 || !idPattern.MatchString(c.RunID) || (c.Kind != "neon" && c.Kind != "supabase") || !scopePattern.MatchString(c.Project) || !strings.HasPrefix(c.Project, "native-") || c.Environment != "development" || c.Context != "k3d-hakopod-dev" || c.ClusterUID == "" || len(c.ClusterUID) > 64 || !digestPattern.MatchString(c.BinarySHA256) {
		return fmt.Errorf("acceptance configuration identity is invalid")
	}
	minimum := 2
	if c.Kind == "neon" {
		minimum = 3
	}
	if len(c.NodeUIDs) < minimum || len(c.NodeUIDs) > 8 || len(c.SourceFiles) < 10 || len(c.SourceFiles) > 8192 || !filepath.IsAbs(c.SourceRoot) || filepath.Clean(c.SourceRoot) != c.SourceRoot || !filepath.IsAbs(c.Kubeconfig) || filepath.Clean(c.Kubeconfig) != c.Kubeconfig || !c.ExpiresAt.After(now) || c.ExpiresAt.After(now.Add(4*time.Hour)) {
		return fmt.Errorf("acceptance configuration bounds are invalid")
	}
	uids := map[string]bool{}
	for name, uid := range c.NodeUIDs {
		if len(name) < 1 || len(name) > 253 || uid == "" || len(uid) > 64 || uids[uid] {
			return fmt.Errorf("acceptance node identity is invalid")
		}
		uids[uid] = true
	}
	for name, digest := range c.SourceFiles {
		if len(name) > 4096 || strings.Count(name, "/") > 32 || filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || !digestPattern.MatchString(digest) {
			return fmt.Errorf("acceptance source identity is invalid")
		}
	}
	return nil
}

func verifySources(c configuration) error { return verifySourcesContext(context.Background(), c) }

func verifySourcesContext(ctx context.Context, c configuration) error {
	seen := make(map[string]bool, len(c.SourceFiles))
	var total int64
	entries := 0
	err := filepath.WalkDir(c.SourceRoot, func(path string, entry os.DirEntry, err error) error {
		entries++
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entries > 16384 || len(path) > len(c.SourceRoot)+4097 || strings.Count(strings.TrimPrefix(path, c.SourceRoot), string(filepath.Separator)) > 33 {
			return fmt.Errorf("acceptance source traversal exceeded its bound")
		}
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("acceptance source is unavailable or symbolic")
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(c.SourceRoot, path)
		if err != nil {
			return fmt.Errorf("acceptance source path is invalid")
		}
		expected, exists := c.SourceFiles[name]
		if !exists || seen[name] || len(seen) >= len(c.SourceFiles) {
			return fmt.Errorf("acceptance source inventory changed")
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("acceptance source identity is unavailable")
		}
		total += info.Size()
		if total > 192<<20 {
			return fmt.Errorf("acceptance source exceeded its total bound")
		}
		actual, err := hashFile(path, 64<<20)
		if err != nil || actual != expected {
			return fmt.Errorf("acceptance source identity changed")
		}
		seen[name] = true
		return nil
	})
	if err == nil && len(seen) != len(c.SourceFiles) {
		return fmt.Errorf("acceptance source inventory is incomplete")
	}
	return err
}

func decodeConfiguration(input io.Reader) (configuration, error) {
	var c configuration
	d := json.NewDecoder(input)
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return c, fmt.Errorf("acceptance configuration is malformed")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] != nil || len(fields) >= 14 {
			return c, fmt.Errorf("acceptance configuration has duplicate or excess fields")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return c, fmt.Errorf("acceptance configuration is malformed")
		}
		fields[name] = value
	}
	closing, err := d.Token()
	if err != nil || closing != json.Delim('}') || d.Decode(&struct{}{}) != io.EOF {
		return c, fmt.Errorf("acceptance configuration is malformed")
	}
	for name, maximum := range map[string]int{"node_uids": 8, "source_files": 8192} {
		if err := uniqueStringMap(fields[name], maximum); err != nil {
			return c, err
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return c, fmt.Errorf("acceptance configuration is malformed")
	}
	strict := json.NewDecoder(strings.NewReader(string(encoded)))
	strict.DisallowUnknownFields()
	if strict.Decode(&c) != nil {
		return c, fmt.Errorf("acceptance configuration has unknown fields")
	}
	return c, nil
}

func uniqueStringMap(raw json.RawMessage, maximum int) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("acceptance identity map is malformed")
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] || len(seen) >= maximum {
			return fmt.Errorf("acceptance identity map has duplicate or excess keys")
		}
		var value string
		if err := d.Decode(&value); err != nil {
			return fmt.Errorf("acceptance identity map value is malformed")
		}
		seen[name] = true
	}
	closing, err := d.Token()
	if err != nil || closing != json.Delim('}') || d.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("acceptance identity map is malformed")
	}
	return nil
}

// Configure admits one source-bound, time-limited development run. This file
// is excluded from shipping builds; clients cannot request this mode.
func Configure(ctx context.Context) (string, error) {
	path := os.Getenv("HAKOPOD_NATIVE_ACCEPTANCE_CONFIG_FILE")
	if path == "" {
		return "", nil
	}
	if active.Load() != nil {
		return "", fmt.Errorf("acceptance was already configured")
	}
	rawConfig, configIdentity, err := readIdentity(path, 2<<20, true)
	if err != nil {
		return "", err
	}
	c, err := decodeConfiguration(bytes.NewReader(rawConfig))
	if err != nil {
		return "", err
	}
	if err = validate(c, time.Now()); err != nil {
		return "", err
	}
	if os.Getenv("HAKOPOD_DEPLOYMENT_MODE") != "" && os.Getenv("HAKOPOD_DEPLOYMENT_MODE") != "self-hosted" {
		return "", fmt.Errorf("native acceptance requires self-hosted mode")
	}
	host, _, err := net.SplitHostPort(os.Getenv("HAKOPOD_LISTEN"))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || os.Getenv("HAKOPOD_TRUST_PROXY") == "true" || os.Getenv("HAKOPOD_KUBECONFIG") != c.Kubeconfig {
		return "", fmt.Errorf("native acceptance requires its loopback API and exact development kubeconfig")
	}
	if os.Getenv("HAKOPOD_DATABASE_URL") != "" {
		return "", fmt.Errorf("native acceptance requires a protected disposable control database reference")
	}
	raw, dsnIdentity, err := readIdentity(os.Getenv("HAKOPOD_DATABASE_URL_FILE"), 16<<10, true)
	if err != nil {
		return "", err
	}
	u, parseErr := url.Parse(strings.TrimSpace(string(raw)))
	if parseErr != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.Path != "/hakopod_acceptance_"+c.RunID {
		return "", fmt.Errorf("native acceptance control database is not the exact disposable run database")
	}
	binary, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("acceptance binary identity is unavailable")
	}
	_, binaryIdentity, err := readIdentity(binary, 128<<20, false)
	if err != nil || binaryIdentity.digest != c.BinarySHA256 {
		return "", fmt.Errorf("acceptance binary identity changed")
	}
	if err = verifySourcesContext(ctx, c); err != nil {
		return "", err
	}
	kubeBytes, kubeIdentity, err := readIdentity(c.Kubeconfig, 2<<20, true)
	if err != nil {
		return "", err
	}
	config, err := clientcmd.Load(kubeBytes)
	if err != nil || config.CurrentContext != c.Context {
		return "", fmt.Errorf("acceptance Kubernetes context changed")
	}
	selected, exists := config.Contexts[c.Context]
	if !exists || selected == nil {
		return "", fmt.Errorf("acceptance Kubernetes context is unavailable")
	}
	clusterConfig, exists := config.Clusters[selected.Cluster]
	if !exists || clusterConfig == nil || clusterConfig.CertificateAuthority != "" || clusterConfig.InsecureSkipTLSVerify || len(clusterConfig.CertificateAuthorityData) == 0 {
		return "", fmt.Errorf("acceptance Kubernetes trust must be embedded and verified")
	}
	identity, exists := config.AuthInfos[selected.AuthInfo]
	if !exists || identity == nil || identity.Exec != nil || identity.AuthProvider != nil || identity.TokenFile != "" || identity.ClientCertificate != "" || identity.ClientKey != "" {
		return "", fmt.Errorf("acceptance Kubernetes credentials must use the protected embedded configuration")
	}
	runtimeConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return "", fmt.Errorf("acceptance Kubernetes client configuration is invalid")
	}
	runtimeConfig.Timeout = 10 * time.Second
	runtimeConfig.QPS = 2
	runtimeConfig.Burst = 2
	kube, err := kubernetes.NewForConfig(runtimeConfig)
	if err != nil {
		return "", fmt.Errorf("acceptance Kubernetes client is unavailable")
	}
	c.guard = &runGuard{files: []fileIdentity{configIdentity, dsnIdentity, binaryIdentity, kubeIdentity}, kube: kube, rest: rest.CopyConfig(runtimeConfig)}
	if err = verifyRun(ctx, c); err != nil {
		return "", err
	}
	active.Store(&c)
	return c.Kind, nil
}

// KubernetesConfig carries the verified bytes into the runtime client, so its
// constructor never reopens a path that could change between verification and use.
func KubernetesConfig() *rest.Config {
	c := active.Load()
	if c == nil || c.guard == nil || c.guard.rest == nil || !time.Now().Before(c.ExpiresAt) {
		return nil
	}
	return rest.CopyConfig(c.guard.rest)
}

func verifyRun(parent context.Context, c configuration) error {
	if c.guard == nil || len(c.guard.files) != 4 || c.guard.kube == nil {
		return fmt.Errorf("acceptance identity guard is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	for _, identity := range c.guard.files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := identity.verify(); err != nil {
			return err
		}
	}
	if err := verifySourcesContext(ctx, c); err != nil {
		return err
	}
	ns, err := c.guard.kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil || string(ns.UID) != c.ClusterUID {
		return fmt.Errorf("acceptance cluster identity changed")
	}
	for name, uid := range c.NodeUIDs {
		node, err := c.guard.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil || string(node.UID) != uid || node.DeletionTimestamp != nil {
			return fmt.Errorf("acceptance node identity changed")
		}
	}
	return ctx.Err()
}

func Plan(project, environment, kind string, plan managedplatform.Plan) managedplatform.Plan {
	if Recovery(project, environment, kind) {
		plan.Capability.Available = true
		plan.Capability.ClusterQualified = true
		plan.Capability.PublicQualified = false
		plan.Capability.Reason = "Development acceptance run; shipping qualification remains unavailable"
	}
	return plan
}

func Recovery(project, environment, kind string) bool {
	c := active.Load()
	return c != nil && time.Now().Before(c.ExpiresAt) && project == c.Project && environment == c.Environment && kind == c.Kind
}

func Watch(ctx context.Context, stop context.CancelFunc) {
	watch(ctx, stop, 30*time.Second, verifyRun)
}

func watch(ctx context.Context, stop context.CancelFunc, interval time.Duration, verify func(context.Context, configuration) error) {
	c := active.Load()
	if c == nil {
		return
	}
	timer := time.NewTimer(time.Until(c.ExpiresAt))
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer active.CompareAndSwap(c, nil)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			stop()
			return
		case <-ticker.C:
			bounded, cancel := context.WithDeadline(ctx, c.ExpiresAt)
			err := verify(bounded, *c)
			cancel()
			if err != nil || !time.Now().Before(c.ExpiresAt) {
				active.CompareAndSwap(c, nil)
				stop()
				return
			}
		}
	}
}
