// Package contract embeds the versioned REST contract in the management binary.
package contract

import _ "embed"

//go:embed openapi.json
var OpenAPI []byte
