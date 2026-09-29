package actions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ParseBitbucketCredential accepts a scoped OAuth access token or the native
// Atlassian API-token pair. Runner registration credentials are a different
// private envelope and are never accepted as management credentials.
func ParseBitbucketCredential(value string) (BitbucketCredential, error) {
	invalid := errors.New("supply a scoped Bitbucket OAuth access token or JSON with email and api_token")
	if len(value) == 0 || len(value) > 8<<10 {
		return BitbucketCredential{}, invalid
	}
	value = strings.TrimSpace(value)
	credential := BitbucketCredential{AccessToken: value}
	if strings.HasPrefix(value, "{") {
		var token struct {
			Email    string `json:"email"`
			APIToken string `json:"api_token"`
		}
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&token) != nil || decoder.Decode(new(any)) != io.EOF {
			return BitbucketCredential{}, invalid
		}
		credential = BitbucketCredential{Email: token.Email, APIToken: token.APIToken}
	}
	if _, err := bitbucketAuthorization(credential); err != nil {
		return BitbucketCredential{}, invalid
	}
	return credential, nil
}

// Validate binds private native bootstrap material to the original repository
// and durable slot. Workspace runners are not a repository isolation boundary.
func (c BitbucketManagerConfig) Validate(target ProviderTarget, name string) error {
	target, err := target.Canonical()
	if err != nil || target.Provider != ProviderBitbucket || target.Bitbucket.Repository == "" || c.SchemaVersion != 1 || c.Target != *target.Bitbucket || c.Name != name || !bitbucketOwnedName.MatchString(name) {
		return errors.New("Bitbucket registration does not match its original repository and runner slot")
	}
	id, ok := canonicalProviderUUID(c.RunnerID)
	if !ok || id != c.RunnerID || !bitbucketPrivateString(c.OAuthClientID, 1, 256) || !bitbucketPrivateString(c.OAuthSecret, 16, 4096) || (c.TokenEndpoint != "" && c.TokenEndpoint != "https://bitbucket.org/site/oauth2/access_token") || !bitbucketOptionalPrivateString(c.Audience, 2048) {
		return errors.New("Bitbucket native registration material is invalid")
	}
	return nil
}

// ParseBitbucketManagerConfig only accepts the bounded private envelope written
// by Register. Its errors never include credential contents.
func ParseBitbucketManagerConfig(data []byte, target ProviderTarget, name string) (BitbucketManagerConfig, error) {
	invalid := errors.New("Bitbucket private registration envelope is invalid")
	if len(data) == 0 || len(data) > 16<<10 {
		return BitbucketManagerConfig{}, invalid
	}
	var config BitbucketManagerConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return BitbucketManagerConfig{}, invalid
	}
	if err := config.Validate(target, name); err != nil {
		return BitbucketManagerConfig{}, err
	}
	return config, nil
}
