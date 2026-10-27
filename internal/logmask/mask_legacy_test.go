package logmask

// Reference copy of the unfiltered masker from before the prefilters, for differential tests.

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var legacyAssignment = regexp.MustCompile(`(?i)(["']?(?:[a-z0-9]+[_-])?(?:password|passwd|pwd|token|api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|secret|secret[_-]?key|client[_-]?secret|session[_-]?token|session[_-]?key|signature|sig|credential|authorization|proxy[_-]?authorization|cookie|set-cookie|x-api-key)["']?[ \t]*[:=][ \t]*)("(?:\\.|[^"\\])*"|'[^']*'|[^\s,;&"'<>]+)`)
var legacyFlag = regexp.MustCompile(`(?i)(--(?:password|passwd|token|api-key|api_token|access-token|client-secret|secret)[ \t]+)("(?:\\.|[^"\\])*"|'[^']*'|[^\s,;&"'<>]+)`)
var legacyHeader = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key)[ \t]*:[ \t]*)([^\r\n]+)`)
var legacyBearer = regexp.MustCompile(`(?i)\b(Bearer|Basic)[ \t]+[A-Za-z0-9._~+/=-]+`)
var legacyToken = regexp.MustCompile(`(?:github_pat_[A-Za-z0-9_]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|gl(?:pat|rt|cbt|dt|ptt)-[A-Za-z0-9_.-]{8,}|(?:AKIA|ASIA)[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
var legacyUserinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+@`)
var legacyPrivateBegin = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
var legacyPrivateEnd = regexp.MustCompile(`-----END (?:[A-Z0-9]+ )?PRIVATE KEY-----`)
var legacyAnsi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
var legacyControls = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
var legacyGitlabSection = regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z )?section_(?:start:[0-9]{1,13}:[A-Za-z0-9_.-]{1,255}(?:\[collapsed=(?:true|false)\])?\r[^\r\n]*|end:[0-9]{1,13}:[A-Za-z0-9_.-]{1,255}\r)\r?$`)

type legacyMasker struct {
	values     map[string]struct{}
	bytes      int
	replacer   *strings.Replacer
	failed     bool
	privateKey bool
	dynamic    []string
}

func legacySensitiveName(name string) bool { return legacyAssignment.MatchString(name + "=x") }

func legacyNew(secrets ...string) *legacyMasker {
	m := &legacyMasker{values: map[string]struct{}{}}
	for _, value := range secrets {
		m.Add(value)
	}
	return m
}

// Invalidate withholds subsequent output when a skipped physical record may
// have contained a mask command. Missing redaction state never means no secrets.
func (m *legacyMasker) learn(v string) {
	if len(m.dynamic) < 256 && len(v) <= MaxLineBytes {
		m.dynamic = append(m.dynamic, v)
	}
}

func (m *legacyMasker) Invalidate() {
	m.failed = true
	m.values = nil
	m.replacer = nil
}

func (m *legacyMasker) add(value string) {
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
func (m *legacyMasker) Add(value string) {
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
			if legacyAssignment.MatchString(key+"=x") && json.Unmarshal(raw, &part) == nil {
				m.addForms(part)
			}
		}
	}
}

func (m *legacyMasker) addForms(value string) {
	if value == "" || m.failed {
		return
	}
	m.add(value)
	// Clients strip terminal controls before rendering. Register that spelling
	// too so a control-bearing configured secret cannot reappear after cleanup.
	m.add(strings.ReplaceAll(legacyControls.ReplaceAllString(legacyAnsi.ReplaceAllString(value, ""), ""), "\r", ""))
	m.add(url.QueryEscape(value))
	m.add(url.PathEscape(value))
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		m.add(encoding.EncodeToString([]byte(value)))
	}
	if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
		m.add(string(encoded[1 : len(encoded)-1]))
	}
}

func (m *legacyMasker) replaceKnown(line string) string {
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
func (m *legacyMasker) Line(line string) string {
	if len(line) > MaxLineBytes {
		m.failed = true
		return withheld
	}
	line = legacyControls.ReplaceAllString(legacyAnsi.ReplaceAllString(line, ""), "")
	keepCR := strings.ContainsRune(line, '\r') && legacyGitlabSection.MatchString(line)
	if !keepCR {
		line = strings.ReplaceAll(line, "\r", "")
	}
	if at := strings.Index(line, "::add-mask::"); at >= 0 {
		value := strings.NewReplacer("%0D", "\r", "%0A", "\n", "%25", "%").Replace(line[at+len("::add-mask::"):])
		m.Add(value)
		m.learn(value)
		for _, word := range strings.Fields(value) {
			m.add(word)
			m.learn(word)
		}
		line = line[:at] + "::add-mask::" + Replacement
	}
	if legacyPrivateBegin.MatchString(line) {
		m.privateKey = true
	}
	if m.privateKey {
		if legacyPrivateEnd.MatchString(line) {
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

func (m *legacyMasker) redactText(line string) string {
	line = m.replaceKnown(line)
	line = legacyUserinfo.ReplaceAllString(line, "${1}"+Replacement+"@")
	line = legacyBearer.ReplaceAllStringFunc(line, func(value string) string { return strings.Fields(value)[0] + " " + Replacement })
	line = legacyToken.ReplaceAllString(line, Replacement)
	line = legacyHeader.ReplaceAllString(line, "${1}"+Replacement)
	line = legacyRedactAssignments(legacyAssignment, line)
	return legacyRedactAssignments(legacyFlag, line)
}

func legacyRedactAssignments(pattern *regexp.Regexp, line string) string {
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
