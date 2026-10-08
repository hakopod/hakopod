// hakopod-session-guard applies hard process limits before the fixed worker starts.
package main

import (
	"fmt"
	"github.com/hakopod/hakopod/internal/sessionguard"
	"os"
)

func main() {
	if err := sessionguard.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Session guard failed:", err)
		os.Exit(1)
	}
}
