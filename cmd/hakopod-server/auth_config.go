package main

import (
	shared "github.com/hakopod/hakopod/auth"
	"github.com/hakopod/hakopod/internal/api"
	"os"
)

func secretSetting(name string) (string, error) { return shared.SecretSettingFrom(name, os.Getenv) }
func secretSettingFrom(name string, get func(string) string) (string, error) {
	return shared.SecretSettingFrom(name, get)
}
func authConfig() (api.AuthConfig, error) { return authConfigFrom(os.Getenv) }
func authConfigFrom(get func(string) string) (api.AuthConfig, error) {
	return shared.ConfigFrom(get, cloudSignupAvailable)
}
