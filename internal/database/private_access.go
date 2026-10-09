package database

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type PrivateAccessInput struct {
	Location    string `json:"location"`
	Endpoint    string `json:"endpoint"`
	SSHHost     string `json:"ssh_host,omitempty"`
	KubeContext string `json:"kube_context,omitempty"`
	LocalPort   int    `json:"local_port,omitempty"`
}

type PrivateAccessStep struct {
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
	Command     string `json:"command,omitempty"`
}

type PrivateAccessGuide struct {
	SchemaVersion int                 `json:"schema_version"`
	DatabaseID    string              `json:"database_id"`
	Revision      int64               `json:"revision"`
	ObservedAt    time.Time           `json:"observed_at"`
	Location      string              `json:"location"`
	Endpoint      Endpoint            `json:"endpoint"`
	Steps         []PrivateAccessStep `json:"steps"`
	Warnings      []string            `json:"warnings"`
	Blockers      []string            `json:"blockers"`
}

var privateAccessName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:@/-]{0,252}$`)
var privateAccessSSHHost = regexp.MustCompile(`^(?:[a-zA-Z0-9_][a-zA-Z0-9_.-]*@)?[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)

func (in PrivateAccessInput) Validate() error {
	if in.Location != "local" && in.Location != "ssh" && in.Location != "kubernetes" {
		return fmt.Errorf("location must be local, ssh or kubernetes")
	}
	if in.Endpoint == "" || len(in.Endpoint) > 32 {
		return fmt.Errorf("select an observed database endpoint")
	}
	if in.LocalPort != 0 && (in.LocalPort < 1024 || in.LocalPort > 65535) {
		return fmt.Errorf("local_port must be between 1024 and 65535")
	}
	if in.SSHHost != "" && !privateAccessSSHHost.MatchString(in.SSHHost) {
		return fmt.Errorf("ssh_host must be a host or user@host without SSH options")
	}
	if in.KubeContext != "" && !privateAccessName.MatchString(in.KubeContext) {
		return fmt.Errorf("kube_context must name an existing kubectl context without spaces")
	}
	return nil
}

// PrivateAccess uses observed endpoints only. It describes client placement;
// generating commands neither opens a tunnel nor verifies a connection.
func PrivateAccess(d Resource, in PrivateAccessInput, managedCloud bool, now time.Time) (PrivateAccessGuide, error) {
	g := PrivateAccessGuide{SchemaVersion: 1, DatabaseID: d.ID, Revision: d.Revision, ObservedAt: d.Observation.ObservedAt, Location: in.Location, Steps: []PrivateAccessStep{}, Warnings: []string{}, Blockers: []string{}}
	if err := in.Validate(); err != nil {
		return g, err
	}
	for _, endpoint := range d.Observation.Endpoints {
		if endpoint.Purpose == in.Endpoint {
			g.Endpoint = endpoint
			break
		}
	}
	if g.Endpoint.Host == "" {
		return g, fmt.Errorf("this endpoint is not present in the database observation")
	}
	if d.Status != "ready" || d.Observation.Status != "ready" || d.Observation.Revision != d.Revision || now.Sub(d.Observation.ObservedAt) > 2*time.Minute || now.Before(d.Observation.ObservedAt) {
		g.Blockers = append(g.Blockers, "Wait for a current, ready database observation before using these connection steps.")
		return g, nil
	}
	// Endpoint authority comes from the owned database namespace, not input.
	namespace := "hdb-" + d.ID
	suffix := "." + namespace + ".svc"
	host := strings.TrimSuffix(g.Endpoint.Host, ".cluster.local")
	if !strings.HasSuffix(host, suffix) || g.Endpoint.Port < 1 || g.Endpoint.Port > 65535 {
		return g, fmt.Errorf("the observed private endpoint cannot be mapped to this database service")
	}
	service := strings.TrimSuffix(host, suffix)
	if strings.Contains(service, ".") || !privateAccessName.MatchString(service) {
		return g, fmt.Errorf("this endpoint requires member-aware private networking")
	}
	if in.Location != "kubernetes" && (d.Spec.Engine == "mongodb" || d.Spec.Engine == "redis" && d.Spec.Mode == "cluster") {
		g.Blockers = append(g.Blockers, "This cluster client discovers additional members. A single-port tunnel cannot carry that traffic. Run the client inside an allowed Kubernetes workload, or use the database's supported public endpoint.")
		return g, nil
	}
	if managedCloud && in.Location != "kubernetes" {
		g.Blockers = append(g.Blockers, "Cloud workspaces do not receive SSH or Kubernetes credentials for shared hosts. Use a dedicated public database endpoint with your source IP allowed, or connect from a bound application.")
		return g, nil
	}
	port := g.Endpoint.Port
	connectHost := g.Endpoint.Host
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if in.Location != "kubernetes" {
		if in.KubeContext == "" {
			g.Blockers = append(g.Blockers, "Enter the Kubernetes context available on the SSH host. The wizard does not grant cluster access.")
		}
		if in.Location == "local" && in.SSHHost == "" {
			g.Blockers = append(g.Blockers, "Enter the SSH host that can reach the Kubernetes API and has your authorized kubectl context.")
		}
		if len(g.Blockers) != 0 {
			return g, nil
		}
		port = in.LocalPort
		if port == 0 {
			port = 15432
		}
		forward := "kubectl --context " + q(in.KubeContext) + " --namespace " + q(namespace) + " port-forward --address 127.0.0.1 " + q("service/"+service) + " " + q(strconv.Itoa(port)+":"+strconv.Itoa(g.Endpoint.Port))
		g.Steps = append(g.Steps, PrivateAccessStep{Title: "Start the database forward on the SSH host", Instruction: "Run this in an existing SSH session on the host with Kubernetes access. Keep it open. The listener accepts loopback traffic only.", Command: forward})
		if in.Location == "local" {
			g.Steps = append(g.Steps, PrivateAccessStep{Title: "Forward to your computer", Instruction: "Run this in a second terminal on your own computer. Keep both forwards running while the client is connected.", Command: "ssh -N -o ExitOnForwardFailure=yes -L " + q("127.0.0.1:"+strconv.Itoa(port)+":127.0.0.1:"+strconv.Itoa(port)) + " -- " + q(in.SSHHost)})
		}
		connectHost = "127.0.0.1"
		g.Warnings = append(g.Warnings, "Forwards select a database pod and close when that pod is replaced. Restart the forward after a rollout or failover.")
	} else {
		g.Steps = append(g.Steps, PrivateAccessStep{Title: "Use an allowed application", Instruction: "Run the client inside the application container bound to this database. The binding supplies private network access. A pod in another namespace does not automatically receive access. Do not give the application a kubeconfig."})
	}
	tls := d.Spec.TLS != nil && d.Spec.TLS.Mode == "required"
	if tls {
		g.Steps = append(g.Steps, PrivateAccessStep{Title: "Install database trust", Instruction: "Download the public CA from this database's Connections & security tab. Save it as database-ca.crt on the machine running the client, or use the CA path mounted in the bound application. Keep certificate and hostname verification enabled."})
	} else {
		g.Warnings = append(g.Warnings, "This database endpoint is configured without TLS. An SSH tunnel encrypts only the SSH portion of the connection.")
	}
	g.Steps = append(g.Steps, PrivateAccessStep{Title: "Select your database login", Instruction: "Set DB_USER and DB_NAME in the client shell to your database username and logical database. Retrieve the password through the authorized credentials flow; enter it at the client prompt."})
	command := ""
	switch d.Spec.Engine {
	case "postgresql", "duckdb":
		if d.Spec.Engine == "duckdb" && in.Endpoint != "postgresql" {
			break
		}
		command = "PGSSLMODE=disable "
		if tls {
			command = "PGSSLMODE=verify-full PGSSLROOTCERT=database-ca.crt "
		}
		if in.Location != "kubernetes" {
			command += "PGHOSTADDR=127.0.0.1 "
		}
		command += "psql --host " + q(g.Endpoint.Host) + " --port " + strconv.Itoa(port) + " --username \"${DB_USER:?Set DB_USER}\" --dbname \"${DB_NAME:?Set DB_NAME}\" --password"
	case "redis":
		command = "redis-cli -h " + q(connectHost) + " -p " + strconv.Itoa(port) + " --user \"${DB_USER:?Set DB_USER}\" --askpass"
		if tls {
			command += " --tls --cacert database-ca.crt --sni " + q(g.Endpoint.Host)
		}
		if d.Spec.Mode == "cluster" {
			command += " -c"
		}
		command += " PING"
	case "mysql", "vitess":
		if tls && in.Location != "kubernetes" {
			g.Steps = append(g.Steps, PrivateAccessStep{Title: "Keep the certificate hostname", Instruction: "On the client machine, map " + g.Endpoint.Host + " to 127.0.0.1 in the hosts file while the tunnel is in use. Remove that mapping after closing the tunnel. The MySQL client must use this hostname to verify the certificate."})
			connectHost = g.Endpoint.Host
		}
		command = "mysql --protocol=TCP --host " + q(connectHost) + " --port " + strconv.Itoa(port) + " --user \"${DB_USER:?Set DB_USER}\" --password"
		if tls {
			command += " --ssl-mode=VERIFY_IDENTITY --ssl-ca=database-ca.crt"
		}
		command += " --database \"${DB_NAME:?Set DB_NAME}\""
	}
	if command != "" {
		g.Steps = append(g.Steps, PrivateAccessStep{Title: "Connect from the selected client location", Instruction: "Run this where your client is installed. The command prompts for a password and does not put it in shell history.", Command: command})
	} else {
		g.Steps = append(g.Steps, PrivateAccessStep{Title: "Configure the database client", Instruction: fmt.Sprintf("Connect to %s on port %d. Use the endpoint hostname %s for TLS verification. This engine requires its native driver configuration; use its documented CA or wallet setting and keep hostname verification enabled.", connectHost, port, g.Endpoint.Host)})
		g.Warnings = append(g.Warnings, "This guide provides the network path but does not generate or verify a native client command for this engine and endpoint.")
	}
	return g, nil
}
