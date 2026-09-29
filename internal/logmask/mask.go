// Package logmask removes credentials before logs leave the control plane.
// A Masker belongs to one scoped log read; it never shares secrets across tenants.
package logmask

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Replacement = "***"
const MaxLineBytes = 64 << 10
const maxMasks = 512
const maxMaskBytes = 1 << 20
const withheld = "[log output withheld: redaction limit exceeded]"
const partial = "[incomplete log line withheld]"

var assignment = regexp.MustCompile(`(?i)(["']?(?:[a-z0-9]+[_-])?(?:password|passwd|pwd|token|api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|secret|secret[_-]?key|client[_-]?secret|session[_-]?token|session[_-]?key|signature|sig|credential|authorization|proxy[_-]?authorization|cookie|set-cookie|x-api-key)["']?[ \t]*[:=][ \t]*)("(?:\\.|[^"\\])*"|'[^']*'|[^\s,;&"'<>]+)`)
var flag = regexp.MustCompile(`(?i)(--(?:password|passwd|token|api-key|api_token|access-token|client-secret|secret)[ \t]+)("(?:\\.|[^"\\])*"|'[^']*'|[^\s,;&"'<>]+)`)
var header = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key)[ \t]*:[ \t]*)([^\r\n]+)`)
var bearer = regexp.MustCompile(`(?i)\b(Bearer|Basic)[ \t]+[A-Za-z0-9._~+/=-]+`)
var token = regexp.MustCompile(`(?:github_pat_[A-Za-z0-9_]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|gl(?:pat|rt|cbt|dt|ptt)-[A-Za-z0-9_.-]{8,}|(?:AKIA|ASIA)[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
var userinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+@`)
var privateBegin = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
var privateEnd = regexp.MustCompile(`-----END (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
var controls = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
var gitlabSection = regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z )?section_(?:start:[0-9]{1,13}:[A-Za-z0-9_.-]{1,255}(?:\[collapsed=(?:true|false)\])?\r[^\r\n]*|end:[0-9]{1,13}:[A-Za-z0-9_.-]{1,255}\r)\r?$`)

type Masker struct {
	values     map[string]struct{}
	bytes      int
	replacer   *strings.Replacer
	failed     bool
	privateKey bool
}

func SensitiveName(name string) bool { return assignment.MatchString(name + "=x") }

func New(secrets ...string) *Masker {
	m := &Masker{values: map[string]struct{}{}}
	for _, value := range secrets {
		m.Add(value)
	}
	return m
}

// Invalidate withholds subsequent output when a skipped physical record may
// have contained a mask command. Missing redaction state never means no secrets.
func (m *Masker) Invalidate() {
	m.failed = true
	m.values = nil
	m.replacer = nil
}

func (m *Masker) add(value string) {
	if value == "" || value == Replacement || m.failed {
		return
	}
	if _, found := m.values[value]; found {
		return
	}
	if len(value) > MaxLineBytes || len(m.values) >= maxMasks || m.bytes+len(value) > maxMaskBytes {
		m.failed = true
		m.values = nil
		m.replacer = nil
		return
	}
	m.values[value] = struct{}{}
	m.bytes += len(value)
	m.replacer = nil
}

// Add includes common encodings and multiline fragments. JSON credential
// envelopes contribute their string fields without exposing them to callers.
func (m *Masker) Add(value string) {
	if len(value) > MaxLineBytes {
		m.failed = true
		return
	}
	m.addForms(value)
	for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\r' || r == '\n' }) {
		m.addForms(line)
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.User != nil {
		if password, present := parsed.User.Password(); present {
			m.addForms(password)
		}
	}
	var fields map[string]json.RawMessage
	if len(value) <= 16<<10 && json.Unmarshal([]byte(value), &fields) == nil && len(fields) <= 32 {
		for key, raw := range fields {
			var part string
			if assignment.MatchString(key+"=x") && json.Unmarshal(raw, &part) == nil {
				m.addForms(part)
			}
		}
	}
}

func (m *Masker) addForms(value string) {
	if value == "" || m.failed {
		return
	}
	m.add(value)
	// Clients strip terminal controls before rendering. Register that spelling
	// too so a control-bearing configured secret cannot reappear after cleanup.
	m.add(strings.ReplaceAll(controls.ReplaceAllString(ansi.ReplaceAllString(value, ""), ""), "\r", ""))
	m.add(url.QueryEscape(value))
	m.add(url.PathEscape(value))
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		m.add(encoding.EncodeToString([]byte(value)))
	}
	if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
		m.add(string(encoded[1 : len(encoded)-1]))
	}
}

func (m *Masker) replaceKnown(line string) string {
	if m.failed {
		return withheld
	}
	if m.replacer == nil && len(m.values) > 0 {
		values := make([]string, 0, len(m.values))
		for value := range m.values {
			values = append(values, value)
		}
		sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
		pairs := make([]string, 0, len(values)*2)
		for _, value := range values {
			pairs = append(pairs, value, Replacement)
		}
		m.replacer = strings.NewReplacer(pairs...)
	}
	if m.replacer != nil {
		return m.replacer.Replace(line)
	}
	return line
}

// Line sees complete lines before truncation, parsing or output buffering.
// Dynamic mask commands never enter public output, even when their data is invalid.
func (m *Masker) Line(line string) string {
	if len(line) > MaxLineBytes {
		m.failed = true
		return withheld
	}
	line = controls.ReplaceAllString(ansi.ReplaceAllString(line, ""), "")
	keepCR := strings.ContainsRune(line, '\r') && gitlabSection.MatchString(line)
	if !keepCR {
		line = strings.ReplaceAll(line, "\r", "")
	}
	if at := strings.Index(line, "::add-mask::"); at >= 0 {
		value := strings.NewReplacer("%0D", "\r", "%0A", "\n", "%25", "%").Replace(line[at+len("::add-mask::"):])
		m.Add(value)
		for _, word := range strings.Fields(value) {
			m.add(word)
		}
		line = line[:at] + "::add-mask::" + Replacement
	}
	if privateBegin.MatchString(line) {
		m.privateKey = true
	}
	if m.privateKey {
		if privateEnd.MatchString(line) {
			m.privateKey = false
		}
		return Replacement
	}
	masked := m.redactText(line)
	if keepCR {
		// A section delimiter is useful only while its later removal cannot
		// reconstruct a credential spanning the metadata/title boundary.
		joined := m.redactText(strings.ReplaceAll(line, "\r", ""))
		if strings.ReplaceAll(masked, "\r", "") != joined {
			return joined
		}
	}
	return masked
}

func (m *Masker) redactText(line string) string {
	line = m.replaceKnown(line)
	line = userinfo.ReplaceAllString(line, "${1}"+Replacement+"@")
	line = bearer.ReplaceAllStringFunc(line, func(value string) string { return strings.Fields(value)[0] + " " + Replacement })
	line = token.ReplaceAllString(line, Replacement)
	line = header.ReplaceAllString(line, "${1}"+Replacement)
	line = redactAssignments(assignment, line)
	return redactAssignments(flag, line)
}

func redactAssignments(pattern *regexp.Regexp, line string) string {
	return pattern.ReplaceAllStringFunc(line, func(value string) string {
		parts := pattern.FindStringSubmatch(value)
		if len(parts) != 3 {
			return Replacement
		}
		mask := Replacement
		if len(parts[2]) >= 2 && (parts[2][0] == '"' || parts[2][0] == '\'') {
			mask = parts[2][:1] + Replacement + parts[2][:1]
		}
		return parts[1] + mask
	})
}

// Reader never emits a partial line. A transport failure, byte limit or truncated
// final chunk cannot expose a prefix of a secret that would match when complete.
func (m *Masker) Reader(source io.Reader) io.Reader {
	return &reader{source: bufio.NewReaderSize(source, MaxLineBytes+1), masker: m}
}

// TimestampedReader requires the timestamp boundary supplied by Kubernetes for
// every physical line. A leading fragment cannot be mistaken for a whole record.
func (m *Masker) TimestampedReader(source io.Reader) io.Reader {
	return &reader{source: bufio.NewReaderSize(source, MaxLineBytes+1), masker: m, timestamped: true}
}

type reader struct {
	source      *bufio.Reader
	masker      *Masker
	pending     []byte
	err         error
	timestamped bool
}

func (r *reader) Read(out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 && r.err == nil {
		line, err := r.source.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			r.masker.failed = true
			for err == bufio.ErrBufferFull {
				_, err = r.source.ReadSlice('\n')
			}
			r.pending = []byte(withheld + "\n")
			r.err = err
			break
		}
		if err != nil {
			r.err = err
			if len(line) > 0 {
				r.pending = []byte(partial + "\n")
			}
			break
		}
		text := string(line[:len(line)-1])
		if r.timestamped {
			stamp, _, found := strings.Cut(text, " ")
			if _, parseErr := time.Parse(time.RFC3339Nano, stamp); !found || parseErr != nil {
				r.pending = []byte(partial + "\n")
				continue
			}
		}
		r.pending = []byte(r.masker.Line(text) + "\n")
	}
	if len(r.pending) > 0 {
		n := copy(out, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.err
}
