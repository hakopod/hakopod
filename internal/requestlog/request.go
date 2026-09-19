// Package requestlog decodes the platform's privacy-limited ingress access log.
package requestlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Base is HAProxy's host + path fetch, without the query string. Never log %r,
// %HU, headers, cookies or bodies. Hex encoding keeps hostile paths out of log syntax.
const Format = "HAKOPOD_REQUEST_V1|%pid|%rt|%HM|%HV|%ST|%B|%TR|%Tw|%Tc|%Tr|%Ta|%tsc|%rc|%bc|%ft|%b|%s|%[var(txn.base),hex]|%[ssl_fc_protocol]|%[src,ipmask(24,48)]"
const Syslog = "address:stdout, format:raw, facility:daemon, level:info, length:8192"

type Entry struct {
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	ApplicationID string    `json:"application_id"`
	Application   string    `json:"application"`
	Project       string    `json:"project"`
	Environment   string    `json:"environment"`
	Service       string    `json:"service"`
	Method        string    `json:"method"`
	Host          string    `json:"host"`
	Path          string    `json:"path"`
	Protocol      string    `json:"protocol"`
	Status        int       `json:"status"`
	Bytes         int64     `json:"bytes"`
	RequestMS     int64     `json:"request_ms"`
	QueueMS       int64     `json:"queue_ms"`
	ConnectMS     int64     `json:"connect_ms"`
	ResponseMS    int64     `json:"response_ms"`
	DurationMS    int64     `json:"duration_ms"`
	Termination   string    `json:"termination"`
	Retries       int64     `json:"retries"`
	Connections   int64     `json:"connections"`
	Frontend      string    `json:"frontend"`
	Backend       string    `json:"backend"`
	Server        string    `json:"server"`
	TLS           string    `json:"tls"`
	ClientNetwork string    `json:"client_network"`
	IngressPod    string    `json:"ingress_pod"`
}

func Parse(line, source, pod string) (Entry, error) {
	e := Entry{IngressPod: pod}
	stamp, rest, ok := strings.Cut(line, " ")
	if !ok || len(line) > 16384 {
		return e, fmt.Errorf("invalid access record")
	}
	var err error
	e.Timestamp, err = time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return e, err
	}
	// stdout can have a syslog prefix, but only our exact marker is accepted.
	at := strings.Index(rest, "HAKOPOD_REQUEST_V1|")
	if at < 0 {
		return e, fmt.Errorf("not a request record")
	}
	fields := strings.Split(rest[at:], "|")
	if len(fields) != 21 {
		return e, fmt.Errorf("incomplete access record")
	}
	for _, field := range fields {
		if strings.IndexFunc(field, unicode.IsControl) >= 0 {
			return e, fmt.Errorf("invalid field")
		}
	}
	e.Method, e.Protocol, e.Termination = fields[3], fields[4], fields[12]
	if len(e.Method) > 24 || len(e.Protocol) > 16 {
		return e, fmt.Errorf("invalid method or protocol")
	}
	numbers := []*int64{&e.Bytes, &e.RequestMS, &e.QueueMS, &e.ConnectMS, &e.ResponseMS, &e.DurationMS, &e.Retries, &e.Connections}
	for i, index := range []int{6, 7, 8, 9, 10, 11, 13, 14} {
		*numbers[i], err = strconv.ParseInt(strings.TrimPrefix(fields[index], "+"), 10, 64)
		if err != nil || *numbers[i] < -1 || *numbers[i] > 1<<50 {
			return e, fmt.Errorf("invalid measurement")
		}
	}
	e.Status, err = strconv.Atoi(fields[5])
	if err != nil || e.Status < 0 || e.Status > 599 {
		return e, fmt.Errorf("invalid status")
	}
	e.Frontend, e.Backend, e.Server = fields[15], fields[16], fields[17]
	for _, v := range []string{e.Frontend, e.Backend, e.Server} {
		if len(v) > 256 {
			return e, fmt.Errorf("invalid route")
		}
	}
	base, err := hex.DecodeString(fields[18])
	if fields[18] == "-" {
		base = []byte{}
		err = nil
	}
	if err != nil || len(base) > 2048 {
		return e, fmt.Errorf("invalid request path")
	}
	clean := strings.SplitN(strings.SplitN(string(base), "?", 2)[0], "#", 2)[0]
	if strings.IndexFunc(clean, unicode.IsControl) >= 0 {
		return e, fmt.Errorf("invalid request path")
	}
	host, path, found := strings.Cut(clean, "/")
	if found {
		e.Path = "/" + path
	}
	e.Host = strings.ToLower(host)
	if u, err := url.Parse("http://" + host); err == nil && u.User == nil {
		e.Host = u.Host
	} else {
		e.Host = ""
	}
	e.TLS = fields[19]
	if e.TLS == "-" {
		e.TLS = ""
	}
	if len(e.TLS) > 32 {
		return e, fmt.Errorf("invalid TLS version")
	}
	if ip := net.ParseIP(fields[20]); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			e.ClientNetwork = v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
		} else {
			e.ClientNetwork = ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
		}
	}
	sum := sha256.Sum256([]byte(source + "\n" + line))
	e.ID = hex.EncodeToString(sum[:])
	return e, nil
}
func (e Entry) JSON() []byte { b, _ := json.Marshal(e); return b }
