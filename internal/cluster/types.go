// Package cluster translates validated application revisions into owned
// Kubernetes resources. It deliberately uses bounded direct API calls instead
// of a cluster-wide informer cache in the combined management process.
package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	managedBy  = "app.kubernetes.io/managed-by"
	ownerKey   = "hakopod.io/application-id"
	serviceKey = "hakopod.io/service"
)

type Options struct {
	SupervisorURL      string
	ProxyNamespace     string
	ProxyConfigMap     string
	ProxyRelease       string
	RegistrySecretName func(context.Context, string, string, string) (string, error)
	VirtualNetworks    func(context.Context, string, string, spec.Application) (map[string]string, error)
	AppDomain          string
	IngressClass       string
	RolloutTimeout     time.Duration
	// TLSIssuer must identify an operator-provisioned cert-manager ClusterIssuer.
	// Empty leaves HTTP explicit; the local development cluster uses this mode.
	TLSIssuer       string
	PublicPort      int
	PublicHTTPSPort int
	// PolicySettleTime accounts for asynchronous CNI policy propagation before
	// starting pods. It is a best-effort delay, not a hostile-tenant guarantee.
	PolicySettleTime time.Duration
}

type Client struct {
	execConfig *rest.Config
	clusterCA  []byte
	kube       kubernetes.Interface
	options    Options
	http       *http.Client
}

type Target struct {
	Project, Environment, ApplicationID, OperationID string
	Revision                                         int64
	Spec                                             spec.Application
	Previous                                         *spec.Application
	SharedNetworks                                   map[string]string
	// BeforeStep revalidates operation authority immediately before each
	// privileged reconciliation stage. A cancelled hook prevents new effects.
	BeforeStep func(context.Context) error
}

func beforeStep(ctx context.Context, t Target) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.BeforeStep != nil {
		return t.BeforeStep(ctx)
	}
	return nil
}

type Event struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Service string `json:"service,omitempty"`
}

type Observation struct {
	Status     string          `json:"status"`
	Services   []ServiceStatus `json:"services"`
	ObservedAt time.Time       `json:"observed_at"`
}

type ServiceStatus struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Ready           int32  `json:"ready"`
	Desired         int32  `json:"desired"`
	Image           string `json:"image"`
	URL             string `json:"url,omitempty"`
	InternalAddress string `json:"internal_address,omitempty"`
	Message         string `json:"message,omitempty"`
}

type Node struct {
	AllocatableGPU    int64       `json:"allocatable_gpu"`
	ResourceVersion   string      `json:"resource_version"`
	ControlPlane      bool        `json:"control_plane"`
	Metrics           NodeMetrics `json:"metrics"`
	Name              string      `json:"name"`
	Ready             bool        `json:"ready"`
	Unschedulable     bool        `json:"unschedulable"`
	Architecture      string      `json:"architecture"`
	KubeletVersion    string      `json:"kubelet_version"`
	AllocatableCPU    string      `json:"allocatable_cpu"`
	AllocatableMemory string      `json:"allocatable_memory"`
	Pods              int32       `json:"pods"`
}

func (c *Client) restClient() rest.Interface {
	client := c.kube.CoreV1().RESTClient()
	if concrete, ok := client.(*rest.RESTClient); ok && concrete == nil {
		return nil
	}
	return client
}

func New(kubeconfig string, options Options) (*Client, error) {
	var config *rest.Config
	var err error
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("Kubernetes configuration unavailable: %w", err)
	}
	config.QPS, config.Burst = 10, 20
	// All requests, including log streams, are bounded; clients reconnect follow
	// streams after the transport's 30-second deadline.
	config.Timeout = 30 * time.Second
	config.UserAgent = "hakopod/0.1"
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	if options.RolloutTimeout <= 0 {
		options.RolloutTimeout = 180 * time.Second
	}
	if options.RolloutTimeout > 15*time.Minute {
		options.RolloutTimeout = 15 * time.Minute
	}
	if options.IngressClass == "" {
		options.IngressClass = "haproxy"
	}
	if options.PolicySettleTime <= 0 {
		options.PolicySettleTime = 2 * time.Second
	}
	if options.PolicySettleTime > 10*time.Second {
		options.PolicySettleTime = 10 * time.Second
	}
	client := &http.Client{Transport: registryTransport(), Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return fmt.Errorf("unsupported registry redirect")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}
	ca := config.CAData
	if len(ca) == 0 && config.CAFile != "" {
		ca, err = os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read Kubernetes CA certificate: %w", err)
		}
	}
	execConfig := rest.CopyConfig(config)
	execConfig.Timeout = 0 // Interactive exec is bounded by its session context.
	return &Client{kube: kube, options: options, http: client, clusterCA: ca, execConfig: execConfig}, nil
}

// Namespace is independent of display names, so application renames cannot
// move workloads or collide with a different project's application.
func Namespace(applicationID string) string {
	return "hp-" + ownerID(applicationID)
}

func ownerID(applicationID string) string {
	hash := sha256.Sum256([]byte(applicationID))
	return fmt.Sprintf("%x", hash[:16])
}
