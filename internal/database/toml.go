package database

import (
	"bytes"
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

func Parse(data []byte) (Spec, error) {
	var spec Spec
	if len(data) == 0 || len(data) > 64<<10 {
		return spec, fmt.Errorf("database TOML must be between 1 byte and 64 KiB")
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&spec); err != nil {
		return spec, fmt.Errorf("database TOML contains invalid or unknown fields")
	}
	return spec, spec.Validate()
}
