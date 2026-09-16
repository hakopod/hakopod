package api

import (
	"errors"
	"github.com/hakopod/hakopod/internal/store"
	"strings"
	"testing"
)

func TestBuildValidationIdentifiesInputFields(t *testing.T) {
	for _, tc := range []struct {
		path string
		edit func(*buildInput)
	}{
		{"name", func(b *buildInput) { b.Name = "Bad Name" }},
		{"service", func(b *buildInput) { b.Service = "bad-" }},
		{"repository", func(b *buildInput) { b.Repository = "invalid" }},
		{"branch", func(b *buildInput) { b.Branch = "not a branch" }},
		{"context_path", func(b *buildInput) { b.ContextPath = "../outside" }},
		{"dockerfile", func(b *buildInput) { b.Dockerfile = "/Dockerfile" }},
		{"port", func(b *buildInput) { b.Port = 65536 }},
		{"architecture", func(b *buildInput) { b.Architecture = "invalid" }},
		{"size", func(b *buildInput) { b.Size = "invalid" }},
		{"build_secrets", func(b *buildInput) { b.BuildSecrets = map[string]string{"secret": "GITHUB_TOKEN"} }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			input := buildInput{Project: "demo", Environment: "development", Name: "app", Service: "web", Repository: "owner/repo", Branch: "main", ContextPath: ".", Dockerfile: "Dockerfile", Mode: "dockerfile", Port: 8080, Size: "small"}
			tc.edit(&input)
			_, err := normalizeBuild(input)
			if !errors.Is(err, store.ErrInput) || !strings.HasPrefix(err.Error(), "invalid input: "+tc.path+":") {
				t.Fatalf("wanted explicit %s field, got %v", tc.path, err)
			}
		})
	}
}
