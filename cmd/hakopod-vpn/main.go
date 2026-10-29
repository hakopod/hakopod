// hakopod-vpn is the root host agent for private management access. The
// private-access installer module downloads it for the installed release and
// runs `hakopod-vpn sync` from a ten-second timer.
package main

import (
	"fmt"
	"os"

	"golang.zx2c4.com/wireguard/wgctrl"

	"github.com/hakopod/hakopod/internal/hostvpn"
)

// Release builds inject the tag version with -X main.version.
var version = "0.1.0-dev"

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "sync" && os.Args[1] != "version") {
		fmt.Fprintln(os.Stderr, "usage: hakopod-vpn sync|version")
		os.Exit(2)
	}
	if os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "hakopod-vpn sync must run as root")
		os.Exit(1)
	}
	host := hostvpn.Default()
	// Without the wireguard module only the WireGuard parts fail; the
	// firewall and the other connectors still run.
	client, err := wgctrl.New()
	if err == nil {
		host.WG = client
	}
	code := host.Sync(version)
	if client != nil {
		client.Close()
	}
	os.Exit(code)
}
