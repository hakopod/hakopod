package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrEdgeUnsupported = errors.New("the ingress configuration cannot safely apply Hakopod Edge")
var ErrEdgeNotAcknowledged = errors.New("the active ingress workers have not acknowledged Hakopod Edge")

// EdgePolicy is installation-wide, with explicit host and path selectors. Rules
// are evaluated in order; the first match supplies the complete policy.
type EdgePolicy struct {
	Enabled           bool       `json:"enabled"`
	ClientIPSource    string     `json:"client_ip_source"`
	TrustedProxyCIDRs []string   `json:"trusted_proxy_cidrs"`
	ClientIPHeader    string     `json:"client_ip_header"`
	CountryHeader     string     `json:"country_header"`
	Rules             []EdgeRule `json:"rules"`
}

type EdgeRule struct {
	ID                string   `json:"id"`
	Host              string   `json:"host"`
	PathPrefix        string   `json:"path_prefix"`
	AllowCIDRs        []string `json:"allow_cidrs"`
	DenyCIDRs         []string `json:"deny_cidrs"`
	AllowCountries    []string `json:"allow_countries"`
	DenyCountries     []string `json:"deny_countries"`
	RequestsPerSecond int      `json:"requests_per_second"`
}

const (
	edgePolicyAnnotation = "hakopod.io/edge-policy"
	edgeBlockStart       = "# BEGIN hakopod edge"
	edgeBlockEnd         = "# END hakopod edge"
	edgeRuntimeVariable  = "proc.hakopod_edge_revision"
	edgeMaximumRules     = 32
	edgeMaximumList      = 64
	edgeMaximumCIDRs     = 512
	edgeMaximumBytes     = 64 << 10
	edgeTableSize        = 20000
)

// ISO 3166-1 alpha-2. Provider sentinel values such as XX and T1 are unavailable
// geography, never valid countries that can evade a deny list.
const edgeCountryCodes = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW"

var edgeRuleID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var edgePath = regexp.MustCompile(`^/[A-Za-z0-9/._~-]*$`)

func ValidateEdgePolicy(value EdgePolicy) error {
	_, err := NormalizeEdgePolicy(value)
	return err
}

// NormalizeEdgePolicy returns an independent copy, so normalizing an accepted
// request cannot mutate a persisted revision or another caller's editor draft.
func NormalizeEdgePolicy(value EdgePolicy) (EdgePolicy, error) {
	out := value
	if out.ClientIPSource == "" {
		out.ClientIPSource = "connection"
	}
	if out.ClientIPSource != "connection" && out.ClientIPSource != "trusted_proxy" {
		return EdgePolicy{}, fmt.Errorf("edge client_ip_source must be connection or trusted_proxy")
	}
	if len(value.TrustedProxyCIDRs) > 32 {
		return EdgePolicy{}, fmt.Errorf("edge accepts at most 32 trusted proxy CIDRs")
	}
	var err error
	out.TrustedProxyCIDRs, err = normalizeEdgeCIDRs(value.TrustedProxyCIDRs, true)
	if err != nil {
		return EdgePolicy{}, fmt.Errorf("edge trusted_proxy_cidrs: %w", err)
	}
	if out.ClientIPSource == "connection" {
		if len(out.TrustedProxyCIDRs) != 0 || out.ClientIPHeader != "" || out.CountryHeader != "" {
			return EdgePolicy{}, fmt.Errorf("edge connection identity cannot use trusted proxy CIDRs or forwarded identity headers")
		}
	} else {
		if len(out.TrustedProxyCIDRs) == 0 || !slices.Contains([]string{"CF-Connecting-IP", "X-Real-IP"}, out.ClientIPHeader) {
			return EdgePolicy{}, fmt.Errorf("edge trusted_proxy identity requires trusted_proxy_cidrs and CF-Connecting-IP or X-Real-IP")
		}
		if !slices.Contains([]string{"", "CF-IPCountry", "CloudFront-Viewer-Country"}, out.CountryHeader) {
			return EdgePolicy{}, fmt.Errorf("edge country_header must be CF-IPCountry or CloudFront-Viewer-Country")
		}
	}
	if len(value.Rules) > edgeMaximumRules {
		return EdgePolicy{}, fmt.Errorf("edge accepts at most %d rules", edgeMaximumRules)
	}
	out.Rules = make([]EdgeRule, len(value.Rules))
	ids := make(map[string]bool, len(value.Rules))
	selectors := make(map[string]bool, len(value.Rules))
	total := len(out.TrustedProxyCIDRs)
	for i, input := range value.Rules {
		rule := input
		if !edgeRuleID.MatchString(rule.ID) || ids[rule.ID] {
			return EdgePolicy{}, fmt.Errorf("edge rule IDs must be unique, start with a lowercase letter or digit, and contain at most 32 lowercase letters, digits, underscores or hyphens")
		}
		ids[rule.ID] = true
		if hasEdgeControl(rule.Host) || hasEdgeControl(rule.PathPrefix) {
			return EdgePolicy{}, fmt.Errorf("edge rule %s contains a control character", rule.ID)
		}
		rule.Host = strings.ToLower(strings.TrimSpace(rule.Host))
		if len(validation.IsDNS1123Subdomain(rule.Host)) != 0 || strings.Contains(rule.Host, "*") {
			return EdgePolicy{}, fmt.Errorf("edge rule %s requires an exact DNS hostname without a port or wildcard", rule.ID)
		}
		rule.PathPrefix = strings.TrimSpace(rule.PathPrefix)
		if rule.PathPrefix == "" {
			rule.PathPrefix = "/"
		}
		if len(rule.PathPrefix) > 128 || !edgePath.MatchString(rule.PathPrefix) || strings.Contains(rule.PathPrefix, "//") {
			return EdgePolicy{}, fmt.Errorf("edge rule %s path_prefix must be an absolute path of at most 128 ASCII letters, digits, slashes, dots, underscores, tildes or hyphens", rule.ID)
		}
		for _, segment := range strings.Split(rule.PathPrefix, "/") {
			if segment == "." || segment == ".." {
				return EdgePolicy{}, fmt.Errorf("edge rule %s path_prefix cannot contain dot segments", rule.ID)
			}
		}
		selector := rule.Host + "\x00" + rule.PathPrefix
		if selectors[selector] {
			return EdgePolicy{}, fmt.Errorf("edge rules cannot repeat a host and path selector")
		}
		selectors[selector] = true
		if rule.RequestsPerSecond < 0 || rule.RequestsPerSecond > 100000 {
			return EdgePolicy{}, fmt.Errorf("edge rule %s requests_per_second must be 0–100000", rule.ID)
		}
		if rule.AllowCIDRs, err = normalizeEdgeCIDRs(input.AllowCIDRs, false); err != nil {
			return EdgePolicy{}, fmt.Errorf("edge rule %s allow_cidrs: %w", rule.ID, err)
		}
		if rule.DenyCIDRs, err = normalizeEdgeCIDRs(input.DenyCIDRs, false); err != nil {
			return EdgePolicy{}, fmt.Errorf("edge rule %s deny_cidrs: %w", rule.ID, err)
		}
		total += len(rule.AllowCIDRs) + len(rule.DenyCIDRs)
		if total > edgeMaximumCIDRs {
			return EdgePolicy{}, fmt.Errorf("edge accepts at most %d CIDRs across the policy", edgeMaximumCIDRs)
		}
		if rule.AllowCountries, err = normalizeEdgeCountries(input.AllowCountries); err != nil {
			return EdgePolicy{}, fmt.Errorf("edge rule %s allow_countries: %w", rule.ID, err)
		}
		if rule.DenyCountries, err = normalizeEdgeCountries(input.DenyCountries); err != nil {
			return EdgePolicy{}, fmt.Errorf("edge rule %s deny_countries: %w", rule.ID, err)
		}
		if len(rule.AllowCountries)+len(rule.DenyCountries) > 0 && (out.ClientIPSource != "trusted_proxy" || out.CountryHeader == "") {
			return EdgePolicy{}, fmt.Errorf("edge country rules require trusted_proxy identity and a country_header")
		}
		out.Rules[i] = rule
	}
	return out, nil
}

func hasEdgeControl(value string) bool {
	for _, r := range value {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

func normalizeEdgeCIDRs(values []string, trusted bool) ([]string, error) {
	if len(values) > edgeMaximumList {
		return nil, fmt.Errorf("provide at most %d IP addresses or CIDRs per list", edgeMaximumList)
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		if hasEdgeControl(raw) {
			return nil, fmt.Errorf("IP addresses and CIDRs cannot contain control characters")
		}
		value := strings.TrimSpace(raw)
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil || address.Zone() != "" {
				return nil, fmt.Errorf("provide IPv4 or IPv6 addresses or CIDRs without ports or zones")
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("use IPv4 notation instead of IPv4-mapped IPv6 CIDRs")
		}
		if trusted && prefix.Bits() == 0 {
			return nil, fmt.Errorf("a trusted proxy CIDR cannot trust the entire Internet")
		}
		value = prefix.Masked().String()
		if seen[value] {
			return nil, fmt.Errorf("IP address and CIDR lists cannot contain duplicate networks")
		}
		seen[value] = true
		out = append(out, value)
	}
	slices.Sort(out)
	return out, nil
}

func normalizeEdgeCountries(values []string) ([]string, error) {
	if len(values) > edgeMaximumList {
		return nil, fmt.Errorf("provide at most %d countries per list", edgeMaximumList)
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if hasEdgeControl(raw) || len(value) != 2 || !strings.Contains(" "+edgeCountryCodes+" ", " "+value+" ") || seen[value] {
			return nil, fmt.Errorf("country lists require unique ISO 3166-1 alpha-2 codes")
		}
		seen[value] = true
		out = append(out, value)
	}
	slices.Sort(out)
	return out, nil
}

func edgePolicyHash(policy EdgePolicy) string {
	data, _ := json.Marshal(policy)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func edgeOwnedBlock(lines ...string) string {
	return edgeBlockStart + "\n" + strings.Join(lines, "\n") + "\n" + edgeBlockEnd
}

func edgeSnippets(policy EdgePolicy) map[string]string {
	hash := edgePolicyHash(policy)
	return map[string]string{
		"global-config-snippet":   edgeOwnedBlock("set-var " + edgeRuntimeVariable + " str(" + hash + ")"),
		"frontend-config-snippet": edgeFrontendBlock(policy),
		"backend-config-snippet":  edgeOwnedBlock("# Reload acknowledgement " + hash),
	}
}

func edgeFrontendBlock(policy EdgePolicy) string {
	if !policy.Enabled || len(policy.Rules) == 0 {
		return edgeOwnedBlock("# Traffic protection is disabled.")
	}
	lines := []string{"acl hakopod_edge_acme path_beg /.well-known/acme-challenge/"}
	hosts := []string{}
	rate := false
	for _, rule := range policy.Rules {
		if !slices.Contains(hosts, rule.Host) {
			hosts = append(hosts, rule.Host, rule.Host+".")
		}
		rate = rate || rule.RequestsPerSecond > 0
	}
	lines = append(lines, edgeACL("hakopod_edge_host", "req.hdr(host),field(1,:),lower -m str", hosts)...)
	// Keep query bytes unchanged while the URI normalizers examine the path.
	// Reject nested encoding and encoded separators before decoding unreserved
	// path bytes, otherwise upstream decoders could disagree on the selected rule.
	lines = append(lines, "http-request set-var(txn.hakopod_edge_normalize) bool(true) if hakopod_edge_host !hakopod_edge_acme")
	guard := "{ var(txn.hakopod_edge_normalize) -m found }"
	lines = append(lines,
		`http-request deny deny_status 400 if `+guard+` { path -m reg -i '(%2f|%5c|%25|%00|\\)' }`,
		"http-request set-var(txn.hakopod_edge_query) query if "+guard+" { url -m sub ? }",
		`http-request set-query "" if `+guard+" { var(txn.hakopod_edge_query) -m found }",
		"http-request normalize-uri percent-decode-unreserved strict if "+guard,
		"http-request normalize-uri path-merge-slashes if "+guard,
		"http-request normalize-uri path-strip-dot if "+guard,
		"http-request normalize-uri path-strip-dotdot full if "+guard,
		"http-request set-query %[var(txn.hakopod_edge_query)] if "+guard+" { var(txn.hakopod_edge_query) -m found }",
	)
	for i, rule := range policy.Rules {
		name := "hakopod_edge_" + strconv.Itoa(i)
		lines = append(lines,
			"acl "+name+"_host req.hdr(host),field(1,:),lower -m str "+rule.Host+" "+rule.Host+".",
			"acl "+name+"_path path_beg "+rule.PathPrefix,
			"http-request set-var(txn.hakopod_edge_rule) int("+strconv.Itoa(i)+") if !hakopod_edge_acme !{ var(txn.hakopod_edge_rule) -m found } "+name+"_host "+name+"_path",
			"acl "+name+" var(txn.hakopod_edge_rule) -m int eq "+strconv.Itoa(i),
		)
	}
	lines = append(lines, "acl hakopod_edge_protected var(txn.hakopod_edge_rule) -m found")
	if policy.ClientIPSource == "trusted_proxy" {
		header := policy.ClientIPHeader
		lines = append(lines,
			"acl hakopod_edge_trusted fc_src -m ip "+strings.Join(policy.TrustedProxyCIDRs, " "),
			"http-request deny deny_status 403 if hakopod_edge_protected !hakopod_edge_trusted",
			"http-request deny deny_status 403 if hakopod_edge_protected !{ req.hdr_cnt("+header+") eq 1 }",
			"http-request deny deny_status 403 if hakopod_edge_protected !{ req.hdr_ip("+header+") -m found }",
			`http-request deny deny_status 403 if hakopod_edge_protected !{ req.fhdr(`+header+`),lower -m reg '^([0-9]{1,3}\.){3}[0-9]{1,3}$' '^([0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}$' }`,
			"http-request set-var(txn.hakopod_edge_client) req.hdr_ip("+header+") if hakopod_edge_protected",
		)
	} else {
		lines = append(lines, "http-request set-var(txn.hakopod_edge_client) fc_src if hakopod_edge_protected")
	}
	if rate {
		lines = append(lines, fmt.Sprintf("stick-table type string len 80 size %d expire 10s nopurge store http_req_rate(1s)", edgeTableSize))
	}
	if policy.CountryHeader != "" {
		lines = append(lines, edgeACL("hakopod_edge_country_known", "req.fhdr("+policy.CountryHeader+"),upper -m str", strings.Fields(edgeCountryCodes))...)
	}
	for i, rule := range policy.Rules {
		name := "hakopod_edge_" + strconv.Itoa(i)
		if len(rule.DenyCIDRs) > 0 {
			lines = append(lines, edgeACL(name+"_deny_ip", "var(txn.hakopod_edge_client) -m ip", rule.DenyCIDRs)...)
			lines = append(lines, "http-request deny deny_status 403 if "+name+" "+name+"_deny_ip")
		}
		if len(rule.AllowCIDRs) > 0 {
			lines = append(lines, edgeACL(name+"_allow_ip", "var(txn.hakopod_edge_client) -m ip", rule.AllowCIDRs)...)
			lines = append(lines, "http-request deny deny_status 403 if "+name+" !"+name+"_allow_ip")
		}
		if len(rule.AllowCountries)+len(rule.DenyCountries) > 0 {
			header := policy.CountryHeader
			lines = append(lines,
				"http-request deny deny_status 403 if "+name+" !{ req.hdr_cnt("+header+") eq 1 }",
				"http-request deny deny_status 403 if "+name+" !hakopod_edge_country_known",
			)
			if len(rule.DenyCountries) > 0 {
				lines = append(lines, edgeACL(name+"_deny_country", "req.fhdr("+header+"),upper -m str", rule.DenyCountries)...)
				lines = append(lines, "http-request deny deny_status 403 if "+name+" "+name+"_deny_country")
			}
			if len(rule.AllowCountries) > 0 {
				lines = append(lines, edgeACL(name+"_allow_country", "req.fhdr("+header+"),upper -m str", rule.AllowCountries)...)
				lines = append(lines, "http-request deny deny_status 403 if "+name+" !"+name+"_allow_country")
			}
		}
		if rule.RequestsPerSecond > 0 {
			lines = append(lines,
				"http-request set-var-fmt(txn.hakopod_edge_key) "+rule.ID+"|%[var(txn.hakopod_edge_client)] if "+name,
				"http-request track-sc2 var(txn.hakopod_edge_key) if "+name,
				"http-request deny deny_status 503 if "+name+" !{ sc2_tracked }",
				"http-request deny deny_status 429 if "+name+" { sc2_http_req_rate gt "+strconv.Itoa(rule.RequestsPerSecond)+" }",
			)
		}
	}
	return edgeOwnedBlock(lines...)
}

func edgeACL(name, expression string, values []string) []string {
	// HAProxy accepts at most 64 arguments per configuration line. Repeated
	// declarations of the same named ACL combine their patterns with OR.
	lines := []string{}
	for len(values) > 0 {
		n := min(32, len(values))
		lines = append(lines, "acl "+name+" "+expression+" "+strings.Join(values[:n], " "))
		values = values[n:]
	}
	return lines
}

func readEdgePolicy(cm *corev1.ConfigMap) (EdgePolicy, error) {
	text := cm.Annotations[edgePolicyAnnotation]
	if text == "" {
		for _, key := range []string{"global-config-snippet", "frontend-config-snippet", "backend-config-snippet"} {
			if strings.Contains(cm.Data[key], edgeBlockStart) || strings.Contains(cm.Data[key], edgeBlockEnd) {
				return EdgePolicy{}, fmt.Errorf("%w: edge snippet has no saved policy", ErrProxyConflict)
			}
		}
		return NormalizeEdgePolicy(EdgePolicy{})
	}
	if len(text) > edgeMaximumBytes {
		return EdgePolicy{}, fmt.Errorf("%w: saved edge policy exceeds its size bound", ErrProxyConflict)
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	var value EdgePolicy
	if err := decoder.Decode(&value); err != nil {
		return EdgePolicy{}, fmt.Errorf("%w: saved edge policy is invalid", ErrProxyConflict)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return EdgePolicy{}, fmt.Errorf("%w: saved edge policy has trailing data", ErrProxyConflict)
	}
	policy, err := NormalizeEdgePolicy(value)
	if err != nil {
		return EdgePolicy{}, fmt.Errorf("%w: saved edge policy is invalid", ErrProxyConflict)
	}
	for key, block := range edgeSnippets(policy) {
		if !edgeBlockMatches(cm.Data[key], block) {
			return EdgePolicy{}, fmt.Errorf("%w: owned edge snippet changed in %s", ErrProxyConflict, key)
		}
	}
	return policy, nil
}

func edgeBlockMatches(text, block string) bool {
	if strings.Count(text, edgeBlockStart) != 1 || strings.Count(text, edgeBlockEnd) != 1 {
		return false
	}
	return strings.Contains("\n"+text+"\n", "\n"+block+"\n")
}

func setEdgePolicy(cm *corev1.ConfigMap, policy EdgePolicy) error {
	old, err := readEdgePolicy(cm)
	if err != nil {
		return err
	}
	if err := edgeCompatibleConfiguration(cm, policy, old); err != nil {
		return err
	}
	oldBlocks := map[string]string{}
	if cm.Annotations[edgePolicyAnnotation] != "" {
		oldBlocks = edgeSnippets(old)
	}
	for key, block := range edgeSnippets(policy) {
		text := cm.Data[key]
		if previous := oldBlocks[key]; previous != "" {
			text = strings.Replace(text, previous, block, 1)
		} else if text == "" {
			text = block
		} else {
			text = strings.TrimRight(text, "\n") + "\n" + block
		}
		cm.Data[key] = text
	}
	data, err := json.Marshal(policy)
	if err != nil || len(data) > edgeMaximumBytes {
		return fmt.Errorf("edge policy exceeds its serialized size bound")
	}
	cm.Annotations[edgePolicyAnnotation] = string(data)
	return nil
}

func edgeCompatibleConfiguration(cm *corev1.ConfigMap, policy, old EdgePolicy) error {
	if !policy.Enabled || len(policy.Rules) == 0 {
		return nil
	}
	for _, key := range []string{"src-ip-header", "proxy-protocol", "cr-frontend-http", "cr-frontend-https"} {
		if cm.Data[key] != "" {
			return fmt.Errorf("%w: operator setting %s requires separate review before enabling edge policy", ErrEdgeUnsupported, key)
		}
	}
	oldBlocks := map[string]string{}
	if cm.Annotations[edgePolicyAnnotation] != "" {
		oldBlocks = edgeSnippets(old)
	}
	for _, key := range []string{"global-config-snippet", "frontend-config-snippet", "backend-config-snippet"} {
		text := strings.Replace(cm.Data[key], oldBlocks[key], "", 1)
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.Contains(line, "hakopod_edge_") || strings.Contains(line, "track-sc2") || (key == "frontend-config-snippet" && strings.HasPrefix(line, "stick-table ")) {
				return fmt.Errorf("%w: operator snippet conflicts with reserved edge variables or rate tracking", ErrEdgeUnsupported)
			}
			if key == "frontend-config-snippet" {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "http-request" && slices.Contains([]string{"allow", "return", "redirect", "use-service", "tarpit", "silent-drop", "reject"}, fields[1]) {
					return fmt.Errorf("%w: an operator frontend action can finish requests before edge policy runs", ErrEdgeUnsupported)
				}
				if strings.Contains(line, "set-src") || strings.Contains(line, "accept-proxy") || strings.Contains(line, "expect-proxy") || strings.Contains(line, "expect-netscaler-cip") {
					return fmt.Errorf("%w: an operator frontend action can replace the original peer address", ErrEdgeUnsupported)
				}
			}
			if key == "global-config-snippet" && strings.HasPrefix(line, "tune.stick-counters ") {
				fields := strings.Fields(line)
				n, err := strconv.Atoi(fields[1])
				if err != nil || n < 3 {
					return fmt.Errorf("%w: edge rate tracking requires at least three stick counters", ErrEdgeUnsupported)
				}
			}
		}
	}
	return nil
}
