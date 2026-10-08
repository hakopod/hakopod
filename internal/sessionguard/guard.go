package sessionguard

import (
	"bytes"
	"encoding/hex"
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

// runCommand checks the immutable exec target before the helper reads caller bytes.
func runCommand(args []string) ([]string, error) {
	if len(args) >= 3 && args[0] == "run" && args[1] == "--" {
		return args[2:], nil
	}
	if len(args) >= 9 && args[0] == "run" && args[1] == "--expected-pod-uid" && args[3] == "--expected-generation" && args[5] == "--ready-token" && args[7] == "--" {
		if args[2] == "" || args[4] == "" || len(args[2]) > 128 || len(args[4]) > 128 || os.Getenv("HAKOPOD_POD_UID") != args[2] || os.Getenv("HAKOPOD_SESSION_GENERATION") != args[4] {
			return nil, fmt.Errorf("session pod identity or generation changed")
		}
		if decoded, err := hex.DecodeString(args[6]); err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("session ready token must contain 64 hexadecimal characters")
		}
		return args[8:], nil
	}
	return nil, fmt.Errorf("use run -- with the fixed command, or provide expected pod UID, generation and ready token before --")
}
func Run(args []string) error {
	if len(args) == 1 && args[0] == "install" {
		return Install()
	}
	command, err := runCommand(args)
	if err != nil {
		return err
	}
	readyToken := ""
	if len(args) >= 9 && args[1] == "--expected-pod-uid" {
		readyToken = args[6]
	}
	return execute(command, readyToken)
}
