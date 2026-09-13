//go:build hakopod_cloud && !hakopod_selfhosted

package main

import "testing"

func TestCloudBuildHasPublicSignupCapability(t *testing.T) {
	if !cloudSignupAvailable {
		t.Fatal("cloud build lost its configured public signup capability")
	}
}
