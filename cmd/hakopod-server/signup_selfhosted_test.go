//go:build !hakopod_cloud || hakopod_selfhosted

package main

import "testing"

func TestSelfHostedBuildHasNoPublicSignupCapability(t *testing.T) {
	if cloudSignupAvailable {
		t.Fatal("self-hosted build enabled public signup")
	}
}
