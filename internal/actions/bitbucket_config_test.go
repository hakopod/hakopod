package actions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBitbucketPrivateConfigRemainsBoundToItsRepositoryAndSlot(t *testing.T) {
	target := ProviderTarget{Provider: ProviderBitbucket, Bitbucket: &BitbucketTarget{Workspace: "{aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}", Repository: "{bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb}"}}
	config := BitbucketManagerConfig{SchemaVersion: 1, Target: *target.Bitbucket, RunnerID: bitbucketFixtureRunner, Name: bitbucketFixtureName, OAuthClientID: "fixture-client", OAuthSecret: "fixture-private-runner-secret"}
	data, _ := json.Marshal(config)
	if _, err := ParseBitbucketManagerConfig(data, target, config.Name); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*BitbucketManagerConfig){
		"workspace only":      func(c *BitbucketManagerConfig) { c.Target.Repository = "" },
		"other repository":    func(c *BitbucketManagerConfig) { c.Target.Repository = "{eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee}" },
		"other workspace":     func(c *BitbucketManagerConfig) { c.Target.Workspace = "{eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee}" },
		"other slot":          func(c *BitbucketManagerConfig) { c.Name = "hakopod-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" },
		"noncanonical runner": func(c *BitbucketManagerConfig) { c.RunnerID = strings.Trim(c.RunnerID, "{}") },
		"redirect endpoint":   func(c *BitbucketManagerConfig) { c.TokenEndpoint = "https://attacker.invalid/token" },
		"invalid secret":      func(c *BitbucketManagerConfig) { c.OAuthSecret += "\n" },
		"new version":         func(c *BitbucketManagerConfig) { c.SchemaVersion = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			modified := config
			alter(&modified)
			encoded, _ := json.Marshal(modified)
			if _, err := ParseBitbucketManagerConfig(encoded, target, config.Name); err == nil || strings.Contains(err.Error(), config.OAuthSecret) {
				t.Fatal("private envelope was accepted outside its bound scope or leaked a secret")
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("null"), append(append([]byte{}, data...), []byte(" {}")...), []byte(strings.Repeat(" ", 16385)), append([]byte(`{"unknown":true,`), data[1:]...)} {
		if _, err := ParseBitbucketManagerConfig(data, target, config.Name); err == nil {
			t.Fatal("malformed private envelope was accepted")
		}
	}
	target.Bitbucket.Repository = ""
	config.Target = *target.Bitbucket
	if err := config.Validate(target, config.Name); err == nil {
		t.Fatal("workspace target was treated as a dedicated repository boundary")
	}
}
