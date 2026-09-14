package spec

import (
	"fmt"
	"strings"
)

// Build arguments are public configuration committed into reviewed CI workflows.
// Credentials must not be passed through this channel or baked into images.
func ValidateBuildArguments(values map[string]string) error {
	if len(values) > 32 {
		return fmt.Errorf("build_args: at most 32 public build arguments")
	}
	for key, value := range values {
		if !envPattern.MatchString(key) || len(key) > 128 || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, "${{") || strings.Contains(value, "{{") || sensitiveEnv(key, value) {
			return fmt.Errorf("build_args.%s: use a public single-line value of at most 2048 bytes, without credentials or workflow expressions", key)
		}
	}
	return nil
}
