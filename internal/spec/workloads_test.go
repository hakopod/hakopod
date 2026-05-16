package spec

import (
	"strings"
	"testing"
)

func TestCatalogRequirementsAndPersistentConstraints(t *testing.T) {
	for _, template := range Templates() {
		a, err := FromTemplate(template.ID, "template-test", false, 5, "Qwen/Qwen3-0.6B", strings.Repeat("a", 40))
		if err != nil {
			t.Fatalf("%s: %v", template.ID, err)
		}
		s := a.Services["main"]
		if s.Volume == nil || s.Replicas != 1 {
			t.Fatal("template data lacks persistent single-replica constraint")
		}
		s.Replicas = 2
		a.Services["main"] = s
		if _, err = Normalize(a); err == nil {
			t.Fatal("unsafe multi-writer persistent deployment allowed")
		}
	}
	a, _ := FromTemplate("postgresql", "db", false, 5, "", "")
	s := a.Services["main"]
	s.Volume.MountPath = "/etc"
	a.Services["main"] = s
	if _, err := Normalize(a); err == nil {
		t.Fatal("system directory mount allowed")
	}
	if _, err := FromTemplate("vllm", "model", false, 30, "Qwen/Qwen3-0.6B", "main"); err == nil {
		t.Fatal("mutable model revision accepted")
	}
}
