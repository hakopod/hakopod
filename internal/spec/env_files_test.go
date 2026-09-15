package spec

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestEnvironmentFilePrecedenceAndSecretReferences(t *testing.T) {
	data := []byte(`name="files"
env_file=[".env", "production.env"]
[env]
MODE="explicit"
[services.api]
image="python:3.13"
env_file="worker.env"
[services.api.env]
EMPTY=""
[services.worker]
image="python:3.13"
`)
	files := map[string]string{".env": "MODE=base\nREGION=one\nAPI_TOKEN=private-fixture\nEMPTY=default", "production.env": "REGION=two", "worker.env": "MODE=worker\nEMPTY=file"}
	result, err := ImportEnvironmentFiles(data, files, func(string, string) string { return "uploaded-token" })
	if err != nil {
		t.Fatal(err)
	}
	api, worker := EffectiveService(result.Spec, result.Spec.Services["api"]), EffectiveService(result.Spec, result.Spec.Services["worker"])
	if !result.Spec.InjectEnv || api.Env["MODE"] != "worker" || api.Env["REGION"] != "two" || api.Env["EMPTY"] != "" || worker.Env["MODE"] != "explicit" || api.Secrets["API_TOKEN"].Ref != "uploaded-token" {
		t.Fatal("incorrect precedence", api.Env, worker.Env)
	}
	encoded, err := toml.Marshal(result.Spec)
	if err != nil || strings.Contains(string(encoded), "private-fixture") || strings.Contains(string(encoded), "env_file") || result.Secrets["uploaded-token"] != "private-fixture" {
		t.Fatal("import content escaped its secret channel")
	}
	if _, err = Parse(encoded); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentFileRejectsUnsafeOrMissingInputs(t *testing.T) {
	for _, value := range []string{`"../.env"`, `"/etc/passwd"`, `"config/../.env"`, `"C:\\file.env"`, `[]`, `[".env", ".env"]`, `12`} {
		if _, err := EnvironmentFileNames([]byte("name='app'\nenv_file=" + value + "\n[services.web]\nimage='nginx'")); err == nil {
			t.Fatal("invalid file reference accepted", value)
		}
	}
	base := []byte("name='app'\nenv_file='.env'\n[services.web]\nimage='nginx'")
	for _, files := range []map[string]string{nil, {".env": "A=1", "unused": "A=2"}, {".env": "A=1\nA=2"}, {".env": "A=" + strings.Repeat("x", 4097)}, {".env": "TOKEN="}} {
		if _, err := ImportEnvironmentFiles(base, files, func(string, string) string { return "secret-ref" }); err == nil {
			t.Fatal("invalid file set accepted")
		}
	}
	if _, err := ImportEnvironmentFiles([]byte("inject_env=false\n"+string(base)), map[string]string{".env": "A=1"}, nil); err == nil {
		t.Fatal("contradictory injection accepted")
	}
}

func TestDotenvLiteralParsingAndRedactedErrors(t *testing.T) {
	got, err := ParseDotenv("\ufeffexport A=one#two # comment\r\nB='one\ntwo'\nC=\"line\\nnext\" # comment\nEMPTY=\nLITERAL=${HOME}$(command)")
	if err != nil || !reflect.DeepEqual(got, map[string]string{"A": "one#two", "B": "one\ntwo", "C": "line\nnext", "EMPTY": "", "LITERAL": "${HOME}$(command)"}) {
		t.Fatal(got, err)
	}
	for _, value := range []string{"X=\"private-fixture\\", "X='private-fixture", "BAD-KEY=private-fixture", "X=\"private-fixture\" extra", "X=private-fixture\x00"} {
		if _, err := ParseDotenv(value); err == nil || strings.Contains(err.Error(), "private-fixture") {
			t.Fatal("invalid input leaked or accepted")
		}
	}
}

func TestComposeEnvironmentFilesPreserveGraphAndOverride(t *testing.T) {
	yaml := []byte("services:\n  api:\n    image: python:3.13\n    env_file: [.env, override.env]\n    environment: {MODE: explicit}\n    depends_on: [worker]\n    volumes: ['data:/data']\n  worker:\n    image: python:3.13\n    env_file: .env\nvolumes:\n  data: {}\n")
	i := 0
	result, err := ImportComposeWithEnvironmentFiles(yaml, "files", nil, nil, map[string]string{".env": "MODE=base\nAPI_TOKEN=private-fixture", "override.env": "MODE=later"}, func(string, string) string { i++; return fmt.Sprintf("fixture-%d", i) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Spec.Services["api"].Env["MODE"] != "explicit" || result.Spec.Services["worker"].Env["MODE"] != "base" || len(result.Spec.Services["api"].Mounts) != 1 || len(result.Secrets) != 2 || strings.Contains(result.TOML, "private-fixture") {
		t.Fatal("compose environment import incorrect")
	}
}
