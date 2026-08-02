package spec

import _ "embed"

//go:embed showcase.toml
var showcaseTOML []byte

// Showcase is a real, deliberately small public storefront and private catalog
// API. Product and checkout data are explicitly samples; runtime status is not.
func Showcase() (Application, error) { return Parse(showcaseTOML) }
