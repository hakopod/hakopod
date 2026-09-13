package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type certificateUpload struct {
	Hostname       string `json:"hostname"`
	CertificatePEM string `json:"certificate_pem,omitempty"`
	PrivateKeyPEM  string `json:"private_key_pem,omitempty"`
	FromIngress    bool   `json:"from_ingress,omitempty"`
}

// Typed responses deliberately contain metadata only, never certificate bodies.
type certificateMetadata struct {
	Certificate string `json:"certificate"`
	Hostname    string `json:"hostname"`
	MountPath   string `json:"mount_path,omitempty"`
	Source      string `json:"source"`
	Ready       bool   `json:"ready"`
	NotBefore   string `json:"not_before,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Message     string `json:"message,omitempty"`
}

type deliveryMetadata struct {
	PublicTCPPolicy *struct {
		Mode    string `json:"mode"`
		Allowed bool   `json:"allowed"`
		Message string `json:"message"`
	} `json:"public_tcp_policy,omitempty"`
	PublicTCP []struct {
		Port       int32    `json:"port"`
		TargetPort int32    `json:"target_port"`
		Status     string   `json:"status"`
		Message    string   `json:"message"`
		Addresses  []string `json:"addresses,omitempty"`
	} `json:"public_tcp"`
	AWSIdentity *struct {
		Binding        string `json:"binding"`
		RoleARN        string `json:"role_arn"`
		Region         string `json:"region"`
		ServiceAccount string `json:"service_account"`
		TokenAudience  string `json:"token_audience"`
		Status         string `json:"status"`
		AWSVerified    bool   `json:"aws_verified"`
		Message        string `json:"message,omitempty"`
	} `json:"aws_identity"`
	ObservedAt string `json:"observed_at"`
}

func readCertificateInput(hostname, certificateFile, privateKeyFile string, fromIngress bool) (certificateUpload, error) {
	input := certificateUpload{Hostname: hostname, FromIngress: fromIngress}
	if !spec.ValidHostname(hostname) {
		return input, &exitError{2, "--hostname requires a lowercase DNS hostname without a port, path or wildcard"}
	}
	if fromIngress {
		if certificateFile != "" || privateKeyFile != "" {
			return input, &exitError{2, "choose --from-ingress or --certificate-file and --private-key-file"}
		}
		return input, nil
	}
	if certificateFile == "" || privateKeyFile == "" {
		return input, &exitError{2, "certificate-upload requires both --certificate-file and --private-key-file, or --from-ingress"}
	}
	certificate, err := readCertificateFile(certificateFile, "certificate-file", 256<<10)
	if err != nil {
		return input, err
	}
	key, err := readCertificateFile(privateKeyFile, "private-key-file", 32<<10)
	if err != nil {
		return input, err
	}
	input.CertificatePEM, input.PrivateKeyPEM = string(certificate), string(key)
	return input, nil
}

func readCertificateFile(path, field string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, &exitError{2, fmt.Sprintf("--%s must name a nonempty regular file of at most %d KiB", field, limit>>10)}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, &exitError{2, "cannot open --" + field}
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, &exitError{2, "--" + field + " changed while being opened"}
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) < 1 || int64(len(data)) > limit {
		return nil, &exitError{2, "cannot read --" + field + " within its size limit"}
	}
	return data, nil
}

func serviceDeliveryPath(app store.Application, service string) (string, error) {
	if service == "" {
		return "", &exitError{2, "--service is required; select the service that owns the certificate or listener"}
	}
	if _, exists := app.Spec.Services[service]; !exists {
		return "", &exitError{2, "service is not part of the selected application"}
	}
	return "/applications/" + url.PathEscape(app.ID) + "/services/" + url.PathEscape(service), nil
}

func serviceCertificates(ctx context.Context, c *client, app store.Application, service string) (any, error) {
	path, err := serviceDeliveryPath(app, service)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []certificateMetadata `json:"items"`
	}
	err = c.request(ctx, "GET", path+"/certificates", nil, "", &out)
	return out, err
}

func uploadServiceCertificate(ctx context.Context, c *client, app store.Application, service string, input certificateUpload) (certificateMetadata, error) {
	var out certificateMetadata
	path, err := serviceDeliveryPath(app, service)
	if err != nil {
		return out, err
	}
	err = c.request(ctx, "POST", path+"/certificates", input, "", &out)
	return out, err
}

func serviceDelivery(ctx context.Context, c *client, app store.Application, service string) (deliveryMetadata, error) {
	var out deliveryMetadata
	path, err := serviceDeliveryPath(app, service)
	if err != nil {
		return out, err
	}
	err = c.request(ctx, "GET", path+"/delivery", nil, "", &out)
	return out, err
}
