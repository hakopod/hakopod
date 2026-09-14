package api

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestPublicBuildArgumentsAndEscaping(t *testing.T) {
	args := map[string]string{"NEXT_PUBLIC_API_URL": "https://api.example.com", "PUBLIC_LABEL": "one, two ' quoted $(touch /tmp/nope)"}
	if err := spec.ValidateBuildArguments(args); err != nil {
		t.Fatal(err)
	}
	text := workflowBuildArguments(args)
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n")[1:] {
		fields, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(line, "            "))).Read()
		if err != nil || len(fields) != 1 {
			t.Fatal("CSV argument corrupt", err)
		}
	}
	quoted := shellBuildArguments(args, "--build-arg")
	if !strings.Contains(quoted, `'"'"'`) {
		t.Fatal("shell quote not escaped")
	}
	for key, value := range map[string]string{"TOKEN": "credential", "PUBLIC_VALUE": "${{ secrets.TOKEN }}", "PASSWORD": "hidden", "NEWLINE": "line\nnext"} {
		if err := spec.ValidateBuildArguments(map[string]string{key: value}); err == nil {
			t.Fatal("unsafe argument accepted", key)
		}
	}
}
