//go:build !linux

package sessionguard

import "fmt"

func execute([]string, string, bool) error {
	return fmt.Errorf("isolated session workers require Linux")
}
