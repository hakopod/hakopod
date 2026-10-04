package main

import (
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/platformconfig"
)

func configureManagedPlatforms(server *api.Server, path, encodedKey string) error {
	return platformconfig.Attach(server, path, encodedKey, platformconfig.Options{})
}
