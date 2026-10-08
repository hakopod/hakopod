//go:build !linux

package sessionguard

import "fmt"

func execute([]string) error { return fmt.Errorf("isolated session workers require Linux") }
