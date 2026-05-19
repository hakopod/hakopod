package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

type imageReference struct{ registry, repository, reference, canonical string }

// Resolve uses the public OCI Distribution API and verifies the returned digest
// against the received bytes. Anonymous bearer tokens are request-local and are
// never returned, logged, cached across deployments or persisted.
func (c *Client) Resolve(ctx context.Context, application spec.Application) (spec.Application, error) {
	return c.ResolveScoped(ctx, application, "", "")
}

func (c *Client) ResolveScoped(ctx context.Context, application spec.Application, project, environment string) (spec.Application, error) {
	app, err := spec.Normalize(application)
	if err != nil {
		return spec.Application{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	architectures, err := c.architectures(ctx)
	if err != nil {
		return spec.Application{}, err
	}
	resolved := make(map[string]string, len(app.Services))
	for _, name := range spec.Names(app) {
		svc := app.Services[name]
		selectedArchitectures := architectures
		if svc.Architecture != "" {
			present := false
			for _, architecture := range architectures {
				present = present || architecture == svc.Architecture
			}
			if !present {
				return spec.Application{}, fmt.Errorf("services.%s.architecture: no available node supports %s", name, svc.Architecture)
			}
			selectedArchitectures = []string{svc.Architecture}
		}
		cacheKey := svc.Image + "\x00" + svc.RegistryCredential + "\x00" + strings.Join(selectedArchitectures, ",")
		if value, ok := resolved[cacheKey]; ok {
			svc.Image = value
			app.Services[name] = svc
			continue
		}
		var credentials *RegistryCredential
		if svc.RegistryCredential != "" {
			credentials, err = c.RegistryCredential(ctx, project, environment, svc.RegistryCredential, svc.Image)
			if err != nil {
				return spec.Application{}, fmt.Errorf("services.%s.image: scoped registry credential is unavailable or does not match the image registry", name)
			}
		}
		value, err := c.resolveImage(ctx, svc.Image, selectedArchitectures, credentials)
		if err != nil {
			return spec.Application{}, fmt.Errorf("services.%s.image: %w", name, err)
		}
		resolved[cacheKey], svc.Image = value, value
		app.Services[name] = svc
	}
	return app, nil
}

func parseReference(value string) (imageReference, error) {
	var result imageReference
	parts := strings.SplitN(value, "@", 2)
	base := parts[0]
	if len(parts) == 2 {
		result.reference = parts[1]
	}
	if colon := strings.LastIndexByte(base, ':'); colon > strings.LastIndexByte(base, '/') {
		if result.reference == "" {
			result.reference = base[colon+1:]
		}
		base = base[:colon]
	}
	if result.reference == "" {
		result.reference = "latest"
	}
	first, rest, hasSlash := strings.Cut(base, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		result.registry, result.repository = first, rest
	} else {
		result.registry, result.repository = "docker.io", base
		if !hasSlash {
			result.repository = "library/" + base
		}
	}
	if result.registry == "index.docker.io" || result.registry == "registry-1.docker.io" {
		result.registry = "docker.io"
	}
	result.canonical = result.registry + "/" + result.repository
	if result.registry == "docker.io" {
		result.registry = "registry-1.docker.io"
	}
	validRepo := regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
	validTag := regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$|^sha256:[a-f0-9]{64}$`)
	if !validRepo.MatchString(result.repository) || !validTag.MatchString(result.reference) || strings.ContainsAny(result.registry, "@ /\\?#") {
		return result, fmt.Errorf("invalid OCI image reference")
	}
	return result, nil
}

func (c *Client) architectures(ctx context.Context) ([]string, error) {
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 200})
	if err != nil {
		return nil, fmt.Errorf("cannot verify image architecture against cluster nodes: %w", err)
	}
	if nodes.Continue != "" {
		return nil, fmt.Errorf("cluster exceeds supported 200-node limit")
	}
	values := make(map[string]bool)
	for _, node := range nodes.Items {
		if !node.Spec.Unschedulable && node.Status.NodeInfo.Architecture != "" {
			values[node.Status.NodeInfo.Architecture] = true
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no schedulable cluster nodes are available")
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func (c *Client) resolveImage(ctx context.Context, image string, architectures []string, credentials ...*RegistryCredential) (string, error) {
	ref, err := parseReference(image)
	if err != nil {
		return "", err
	}
	endpoint := "https://" + ref.registry + "/v2/" + ref.repository + "/manifests/" + ref.reference
	var credential *RegistryCredential
	if len(credentials) > 0 && credentials[0] != nil {
		copy := *credentials[0]
		copy.Repository = ref.repository
		credential = &copy
		if credential.Registry != ref.registry {
			return "", fmt.Errorf("registry credential does not match the requested host")
		}
	}
	data, token, err := c.registryGet(ctx, endpoint, manifestTypes, "", credential)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	digest := fmt.Sprintf("sha256:%x", hash)
	if strings.HasPrefix(ref.reference, "sha256:") && ref.reference != digest {
		return "", fmt.Errorf("registry response does not match requested image digest")
	}
	var manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Manifests     []struct {
			Platform struct{ Architecture, OS string } `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.SchemaVersion != 2 {
		return "", fmt.Errorf("registry returned an unsupported image manifest; OCI or Docker schema 2 is required")
	}
	supported := make(map[string]bool)
	if len(manifest.Manifests) > 0 {
		for _, item := range manifest.Manifests {
			if item.Platform.OS == "linux" {
				supported[item.Platform.Architecture] = true
			}
		}
	} else {
		if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(manifest.Config.Digest) {
			return "", fmt.Errorf("manifest has no valid image configuration digest")
		}
		config, _, err := c.registryGet(ctx, "https://"+ref.registry+"/v2/"+ref.repository+"/blobs/"+manifest.Config.Digest, "application/octet-stream", token, credential)
		if err != nil {
			return "", fmt.Errorf("cannot verify image architecture: %w", err)
		}
		configHash := sha256.Sum256(config)
		if fmt.Sprintf("sha256:%x", configHash) != manifest.Config.Digest {
			return "", fmt.Errorf("registry configuration failed digest verification")
		}
		var platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		}
		if err := json.Unmarshal(config, &platform); err != nil {
			return "", fmt.Errorf("invalid image platform configuration")
		}
		if platform.OS == "linux" {
			supported[platform.Architecture] = true
		}
	}
	for _, architecture := range architectures {
		if !supported[architecture] {
			return "", fmt.Errorf("image lacks linux/%s support required by a schedulable cluster node", architecture)
		}
	}
	return ref.canonical + "@" + digest, nil
}

func (c *Client) registryGet(ctx context.Context, endpoint, accept, token string, credentials ...*RegistryCredential) ([]byte, string, error) {
	var credential *RegistryCredential
	if len(credentials) > 0 {
		credential = credentials[0]
	}
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, "", fmt.Errorf("invalid registry endpoint")
		}
		request.Header.Set("Accept", accept)
		request.Header.Set("User-Agent", "hakopod/0.1")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		} else if credential != nil {
			if request.URL.Scheme != "https" || request.URL.Host != credential.Registry {
				return nil, "", fmt.Errorf("registry credential target mismatch")
			}
			request.SetBasicAuth(credential.Username, credential.Password)
		}
		response, err := c.http.Do(request)
		if err != nil {
			return nil, "", fmt.Errorf("registry request failed; verify public registry DNS, TLS and connectivity")
		}
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
			challenge := response.Header.Get("WWW-Authenticate")
			response.Body.Close()
			token, err = c.registryToken(ctx, challenge, credential)
			if err != nil {
				return nil, "", err
			}
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
				return nil, "", fmt.Errorf("registry authentication was denied; verify the scoped credential and repository pull permission")
			}
			if response.StatusCode == http.StatusNotFound {
				return nil, "", fmt.Errorf("image repository, tag or digest was not found")
			}
			if response.StatusCode == http.StatusTooManyRequests {
				return nil, "", fmt.Errorf("public registry rate limit reached; retry later")
			}
			return nil, "", fmt.Errorf("registry returned HTTP %d", response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("registry response could not be read")
		}
		if len(data) > 2<<20 {
			return nil, "", fmt.Errorf("registry manifest exceeds 2 MiB limit")
		}
		return data, token, nil
	}
	return nil, "", fmt.Errorf("registry authentication was denied")
}

var bearerField = regexp.MustCompile(`([a-z]+)="([^"]*)"`)

func (c *Client) registryToken(ctx context.Context, challenge string, credentials ...*RegistryCredential) (string, error) {
	var credential *RegistryCredential
	if len(credentials) > 0 {
		credential = credentials[0]
	}
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") || len(challenge) > 8192 {
		return "", fmt.Errorf("registry requires unsupported authentication")
	}
	fields := make(map[string]string)
	for _, item := range bearerField.FindAllStringSubmatch(challenge, -1) {
		fields[item[1]] = item[2]
	}
	realm, err := url.Parse(fields["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" || realm.User != nil {
		return "", fmt.Errorf("registry returned an invalid token endpoint")
	}
	if credential != nil && !credential.trustsRealm(realm) {
		return "", fmt.Errorf("registry requested an untrusted credential token endpoint")
	}
	query := realm.Query()
	for _, key := range []string{"service", "scope"} {
		if fields[key] != "" {
			query.Set(key, fields[key])
		}
	}
	if credential != nil {
		query.Set("scope", "repository:"+credential.Repository+":pull")
	}
	realm.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", fmt.Errorf("invalid registry token request")
	}
	client := *c.http
	if credential != nil {
		request.SetBasicAuth(credential.Username, credential.Password)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("registry token service could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token request was denied; verify repository pull permission")
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("registry returned an invalid token response")
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	if body.Token == "" || len(body.Token) > 32<<10 {
		return "", fmt.Errorf("registry returned an invalid anonymous token")
	}
	return body.Token, nil
}

// registryTransport disallows requests to private, loopback and link-local
// endpoints, including after DNS changes and redirects. Registry lookups accept
// developer input, so they must not become a management-network HTTP proxy.
func registryTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost = 8, 2
	transport.ResponseHeaderTimeout = 15 * time.Second
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid public registry host")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("registry DNS resolution failed")
		}
		for _, ip := range ips {
			if !publicAddress(ip) {
				return nil, fmt.Errorf("registry endpoint must use public IP addresses")
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("registry host has no addresses")
		}
		return nil, last
	}
	return transport
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
