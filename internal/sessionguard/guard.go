package sessionguard

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

const Path = "/run/hakopod-session/guard"
const MaxProcesses = 128
const MaxOpenFiles = 256

// Install writes only the fixed isolated volume path. Existing bytes must match.
func Install() error {
	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the guard executable")
	}
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("cannot read the guard executable")
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		return fmt.Errorf("guard executable exceeds its size limit")
	}
	output, err := os.OpenFile(Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if os.IsExist(err) {
		info, e := os.Lstat(Path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 {
			return fmt.Errorf("existing guard file is not a regular executable")
		}
		existing, e := os.ReadFile(Path)
		if e != nil || !bytes.Equal(existing, data) {
			return fmt.Errorf("existing guard executable does not match")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot create the guard executable")
	}
	ok := false
	defer func() {
		output.Close()
		if !ok {
			os.Remove(Path)
		}
	}()
	if _, err = output.Write(data); err != nil {
		return fmt.Errorf("cannot write the guard executable")
	}
	if err = output.Sync(); err != nil {
		return fmt.Errorf("cannot sync the guard executable")
	}
	ok = true
	return output.Close()
}
func Run(args []string) error {
	if len(args) == 1 && args[0] == "install" {
		return Install()
	}
	if len(args) < 3 || args[0] != "run" || args[1] != "--" {
		return fmt.Errorf("use install or run -- followed by the fixed worker command")
	}
	return execute(args[2:])
}
