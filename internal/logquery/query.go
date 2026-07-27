// Package logquery evaluates a small, bounded SQL-like predicate over log entries.
// Queries never reach a database or a shell. Regular expressions and arbitrary
// functions are deliberately absent so their cost stays predictable.
package logquery

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Entry struct {
	Timestamp time.Time      `json:"timestamp"`
	Pod       string         `json:"pod"`
	Container string         `json:"container"`
	Service   string         `json:"service"`
	Message   string         `json:"message"`
	Severity  string         `json:"severity"`
	Fields    map[string]any `json:"fields"`
	// MetadataOmitted is an internal sampling diagnostic; the original message
	// remains available when structured metadata exceeds safe parsing bounds.
	MetadataOmitted bool `json:"-"`
}

type Predicate func(Entry) bool
type token struct {
	value  string
	quoted bool
}
type parser struct {
	tokens     []token
	pos, depth int
}

func Compile(input string) (Predicate, error) {
	if len(input) > 4096 {
		return nil, fmt.Errorf("query exceeds 4096 characters")
	}
	tokens, err := tokenize(input)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return func(Entry) bool { return true }, nil
	}
	p := parser{tokens: tokens}
	p.take("WHERE")
	result, err := p.or()
	if err == nil && p.pos != len(tokens) {
		err = fmt.Errorf("unexpected token %q", tokens[p.pos].value)
	}
	return result, err
}

func tokenize(input string) ([]token, error) {
	out := []token{}
	for i := 0; i < len(input); {
		if unicode.IsSpace(rune(input[i])) {
			i++
			continue
		}
		if len(out) >= 128 {
			return nil, fmt.Errorf("query exceeds 128 tokens")
		}
		c := input[i]
		if c == '\'' || c == '"' {
			i++
			var b strings.Builder
			closed := false
			for i < len(input) {
				if input[i] == c {
					i++
					if i < len(input) && input[i] == c {
						b.WriteByte(c)
						i++
						continue
					}
					closed = true
					break
				}
				b.WriteByte(input[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted value")
			}
			out = append(out, token{b.String(), true})
			continue
		}
		if strings.ContainsRune("()=<>!,", rune(c)) {
			i++
			value := string(c)
			if i < len(input) && ((strings.ContainsRune("<>!", rune(c)) && input[i] == '=') || c == '<' && input[i] == '>') {
				value += string(input[i])
				i++
			}
			out = append(out, token{value, false})
			continue
		}
		start := i
		for i < len(input) && !unicode.IsSpace(rune(input[i])) && !strings.ContainsRune("()=<>!,\"'", rune(input[i])) {
			i++
		}
		if i == start {
			return nil, fmt.Errorf("invalid query token")
		}
		out = append(out, token{input[start:i], false})
	}
	return out, nil
}
func (p *parser) take(s string) bool {
	if p.pos < len(p.tokens) && !p.tokens[p.pos].quoted && strings.EqualFold(p.tokens[p.pos].value, s) {
		p.pos++
		return true
	}
	return false
}
func (p *parser) or() (Predicate, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.take("OR") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		a, b := left, right
		left = func(e Entry) bool { return a(e) || b(e) }
	}
	return left, nil
}
func (p *parser) and() (Predicate, error) {
	left, err := p.atom()
	if err != nil {
		return nil, err
	}
	for p.take("AND") {
		right, err := p.atom()
		if err != nil {
			return nil, err
		}
		a, b := left, right
		left = func(e Entry) bool { return a(e) && b(e) }
	}
	return left, nil
}

var fieldPattern = regexp.MustCompile(`^json\.[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)

func (p *parser) atom() (Predicate, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 12 {
		return nil, fmt.Errorf("query nesting exceeds 12 levels")
	}
	if p.take("NOT") {
		inner, err := p.atom()
		if err != nil {
			return nil, err
		}
		return func(e Entry) bool { return !inner(e) }, nil
	}
	if p.take("(") {
		inner, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.take(")") {
			return nil, fmt.Errorf("expected closing parenthesis")
		}
		return inner, nil
	}
	if p.pos >= len(p.tokens) {
		return nil, fmt.Errorf("expected a field comparison")
	}
	f := p.tokens[p.pos]
	p.pos++
	field := strings.ToLower(f.value)
	if f.quoted || !(field == "message" || field == "severity" || field == "pod" || field == "service" || field == "container" || field == "timestamp" || fieldPattern.MatchString(f.value)) {
		return nil, fmt.Errorf("unknown field %q; use message, severity, pod, service, container, timestamp or json.field", f.value)
	}
	if strings.HasPrefix(field, "json.") {
		field = f.value
	}
	if p.take("IS") {
		negate := p.take("NOT")
		if !p.take("NULL") {
			return nil, fmt.Errorf("IS supports NULL or NOT NULL")
		}
		return func(e Entry) bool { v := fieldValue(e, field); return (v == nil) != negate }, nil
	}
	negate := p.take("NOT")
	if p.take("IN") {
		if !p.take("(") {
			return nil, fmt.Errorf("IN requires a parenthesized value list")
		}
		values := []token{}
		for {
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			values = append(values, v)
			if len(values) > 32 {
				return nil, fmt.Errorf("IN supports at most 32 values")
			}
			if p.take(")") {
				break
			}
			if !p.take(",") {
				return nil, fmt.Errorf("expected comma in IN list")
			}
		}
		return func(e Entry) bool {
			v := fieldValue(e, field)
			if v == nil {
				return false
			}
			found := false
			for _, t := range values {
				if compare(v, t, "=", field) {
					found = true
					break
				}
			}
			return found != negate
		}, nil
	}
	if p.pos >= len(p.tokens) {
		return nil, fmt.Errorf("expected comparison operator")
	}
	op := strings.ToUpper(p.tokens[p.pos].value)
	quoted := p.tokens[p.pos].quoted
	p.pos++
	if quoted || !(op == "=" || op == "!=" || op == "<>" || op == ">" || op == ">=" || op == "<" || op == "<=" || op == "LIKE" || op == "ILIKE" || op == "CONTAINS") || negate && op != "LIKE" && op != "ILIKE" && op != "CONTAINS" {
		return nil, fmt.Errorf("unsupported comparison operator")
	}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	if op == "LIKE" || op == "ILIKE" {
		var b strings.Builder
		b.WriteString("(?s)^")
		if op == "ILIKE" {
			b.WriteString("(?i)")
		}
		for _, c := range v.value {
			switch c {
			case '%':
				b.WriteString(".*")
			case '_':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		b.WriteByte('$')
		re, err := regexp.Compile(b.String())
		if err != nil {
			return nil, fmt.Errorf("invalid LIKE pattern")
		}
		return func(e Entry) bool {
			x := fieldValue(e, field)
			if x == nil {
				return false
			}
			return re.MatchString(fmt.Sprint(x)) != negate
		}, nil
	}
	return func(e Entry) bool {
		x := fieldValue(e, field)
		if x == nil {
			return false
		}
		return compare(x, v, op, field) != negate
	}, nil
}
func (p *parser) value() (token, error) {
	if p.pos >= len(p.tokens) {
		return token{}, fmt.Errorf("expected a comparison value")
	}
	t := p.tokens[p.pos]
	p.pos++
	if !t.quoted && (strings.ContainsAny(t.value, "()=<>!,") || strings.EqualFold(t.value, "AND") || strings.EqualFold(t.value, "OR")) {
		return token{}, fmt.Errorf("quote the comparison value")
	}
	return t, nil
}
func fieldValue(e Entry, field string) any {
	switch field {
	case "message":
		return e.Message
	case "severity":
		return e.Severity
	case "pod":
		return e.Pod
	case "container":
		return e.Container
	case "service":
		return e.Service
	case "timestamp":
		return e.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	var v any = e.Fields
	for _, part := range strings.Split(strings.TrimPrefix(field, "json."), ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	return v
}

var ranks = map[string]int{"DEFAULT": 0, "TRACE": 50, "DEBUG": 100, "INFO": 200, "NOTICE": 300, "WARNING": 400, "WARN": 400, "ERROR": 500, "CRITICAL": 600, "FATAL": 600, "ALERT": 700, "EMERGENCY": 800}

func compare(value any, expected token, op, field string) bool {
	a, b := fmt.Sprint(value), expected.value
	if op == "CONTAINS" {
		return strings.Contains(a, b)
	}
	cmp := strings.Compare(a, b)
	if field == "severity" {
		x, xok := ranks[strings.ToUpper(a)]
		y, yok := ranks[strings.ToUpper(b)]
		if !xok || !yok {
			return false
		}
		cmp = x - y
	} else if field == "timestamp" {
		x, xerr := time.Parse(time.RFC3339Nano, a)
		y, yerr := time.Parse(time.RFC3339Nano, b)
		if xerr != nil || yerr != nil {
			return false
		}
		cmp = x.Compare(y)
	} else if !expected.quoted {
		x, xe := strconv.ParseFloat(a, 64)
		y, ye := strconv.ParseFloat(b, 64)
		if math.IsNaN(y) || math.IsInf(y, 0) {
			return false
		}
		if ye == nil {
			// A numeric predicate must not match non-finite or non-numeric
			// structured values by accidentally leaving the comparison at zero.
			if xe != nil || math.IsNaN(x) || math.IsInf(x, 0) {
				return false
			}
			cmp = 0
			if x < y {
				cmp = -1
			}
			if x > y {
				cmp = 1
			}
		}
	}
	switch op {
	case "=":
		return cmp == 0
	case "!=", "<>":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	}
	return false
}

const (
	maxMetadataDepth  = 8
	maxMetadataTokens = 128
	maxMetadataBytes  = 64 << 10
)

// metadataAllowed walks tokens before Decode allocates maps/slices. Decoder's
// stack therefore never reaches arbitrary JSON nesting, and no recursive
// object graph is materialized until both depth and token budgets are known.
func metadataAllowed(input string) (allowed, limited bool) {
	input = strings.TrimSpace(input)
	if input == "" || input[0] != '{' {
		return false, false
	}
	if len(input) > maxMetadataBytes {
		return false, true
	}
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	depth, tokens := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return tokens > 0 && depth == 0, false
		}
		if err != nil {
			return false, false
		}
		tokens++
		if tokens > maxMetadataTokens {
			return false, true
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
				if depth > maxMetadataDepth {
					return false, true
				}
			case '}', ']':
				depth--
				if depth == 0 {
					// Require exactly one complete JSON object. A JSON prefix
					// followed by more text does not supply structured fields.
					_, err := decoder.Token()
					return err == io.EOF, false
				}
			}
		}
	}
}

// RetainedBytes conservatively accounts for entry/string storage and parsed
// metadata, including map buckets/interface slots. It is a budget estimate,
// not an RSS measurement; bounds above limit its recursive walk to8 levels.
func (e Entry) RetainedBytes() int {
	return 256 + len(e.Message) + len(e.Pod) + len(e.Service) + len(e.Container) + len(e.Severity) + metadataBytes(e.Fields)
}

func metadataBytes(value any) int {
	switch value := value.(type) {
	case map[string]any:
		size := 256
		for key, child := range value {
			size += 256 + len(key) + metadataBytes(child)
		}
		return size
	case []any:
		size := 64 + 32*len(value)
		for _, child := range value {
			size += metadataBytes(child)
		}
		return size
	case string:
		return 32 + len(value)
	case json.Number:
		return 32 + len(value)
	default:
		return 32
	}
}

// ParseEntry keeps the original message and exposes structured JSON fields when
// the workload emitted them. It never substitutes a guessed timestamp or level.
func ParseEntry(line, service, pod, container string) Entry {
	e := Entry{Service: service, Pod: pod, Container: container, Message: line, Severity: "DEFAULT", Fields: map[string]any{}}
	if stamp, rest, ok := strings.Cut(line, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			e.Timestamp = t
			e.Message = rest
		}
	}
	allowed, limited := metadataAllowed(e.Message)
	e.MetadataOmitted = limited
	if !allowed {
		return e
	}
	dec := json.NewDecoder(strings.NewReader(e.Message))
	dec.UseNumber()
	var fields map[string]any
	if dec.Decode(&fields) == nil && fields != nil {
		e.Fields = fields
		for _, key := range []string{"severity", "level", "log.level"} {
			if level, ok := fields[key].(string); ok {
				level = strings.ToUpper(level)
				if _, ok := ranks[level]; ok {
					if level == "WARN" {
						level = "WARNING"
					}
					e.Severity = level
					break
				}
			}
		}
	}
	return e
}
