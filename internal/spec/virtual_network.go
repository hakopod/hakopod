package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// VirtualNetwork is a project/environment-scoped group of private segments.
// Each segment explicitly lists the applications allowed to join it.
type VirtualNetwork struct {
	SchemaVersion int                       `json:"schema_version" toml:"schema_version"`
	Name          string                    `json:"name" toml:"name"`
	Description   string                    `json:"description" toml:"description"`
	Segments      map[string]NetworkSegment `json:"segments" toml:"segments"`
}

type NetworkSegment struct {
	Applications []string `json:"applications" toml:"applications"`
}

func ParseVirtualNetwork(data []byte) (VirtualNetwork, error) {
	if len(data) > 32<<10 {
		return VirtualNetwork{}, errors.New("virtual network TOML exceeds 32 KiB")
	}
	var value VirtualNetwork
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&value); err != nil {
		return value, errors.New("invalid virtual network TOML: check field names and types")
	}
	return NormalizeVirtualNetwork(value)
}

func NormalizeVirtualNetwork(input VirtualNetwork) (VirtualNetwork, error) {
	data, err := json.Marshal(input)
	if err != nil || len(data) > 32<<10 {
		return VirtualNetwork{}, errors.New("virtual network exceeds 32 KiB")
	}
	var v VirtualNetwork
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	if v.SchemaVersion == 0 {
		v.SchemaVersion = 1
	}
	if v.SchemaVersion != 1 || !namePattern.MatchString(v.Name) {
		return v, errors.New("use schema_version = 1 and a network name of 1–40 lowercase letters, digits or hyphens")
	}
	if v.Name == "new" {
		return v, errors.New("network name new is reserved; choose another name")
	}
	v.Description = strings.TrimSpace(v.Description)
	if !utf8.ValidString(v.Description) || utf8.RuneCountInString(v.Description) > 500 || strings.ContainsFunc(v.Description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return v, errors.New("description: at most 500 characters without control characters")
	}
	if len(v.Segments) < 1 || len(v.Segments) > 16 {
		return v, errors.New("segments: define between 1 and 16 private segments")
	}
	for name, segment := range v.Segments {
		if !namePattern.MatchString(name) || len(segment.Applications) > 64 {
			return v, errors.New("segments: use valid names and at most 64 allowed applications per segment")
		}
		seen := map[string]bool{}
		for _, application := range segment.Applications {
			if !namePattern.MatchString(application) || seen[application] {
				return v, fmt.Errorf("segments.%s.applications: use unique application names, without wildcards", name)
			}
			seen[application] = true
		}
		if segment.Applications == nil {
			segment.Applications = []string{}
		}
		sort.Strings(segment.Applications)
		v.Segments[name] = segment
	}
	return v, nil
}

func (v VirtualNetwork) Allows(segment, application string) bool {
	for _, name := range v.Segments[segment].Applications {
		if name == application {
			return true
		}
	}
	return false
}
