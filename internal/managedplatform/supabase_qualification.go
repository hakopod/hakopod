package managedplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const SupabaseReleaseQualificationID = "supabase-0.8.2-linux-amd64"
const SupabaseReleaseImageInventorySHA256 = "bb146a329003ff44a860626ded38ed71b88c9169856980958c9859148ed1f4ec"

var supabaseReleaseImages = map[string]string{
	"api-gateway":   "docker.io/envoyproxy/envoy@sha256:be87c8b52663c1164a5bdf3c5419017a269cb3d8c74be1ec93638a71f1ffbd4b",
	"auth":          "docker.io/supabase/gotrue@sha256:7e813221b93fbf54b515036438550e483bfaf057b9db52fe9bc1ce91c47e817e",
	"database":      "docker.io/supabase/postgres@sha256:5a4314708484bec672de2c09653a5c01fb1c84a998564ac231b0325e2238ed5b",
	"edge-runtime":  "docker.io/supabase/edge-runtime@sha256:b331c6422f4f4bebd6052357da5f3c87610128c8cb530390ca024eb68cf4bf2e",
	"image-proxy":   "docker.io/darthsim/imgproxy@sha256:80200afeaa85c4abb58bbcb84deadab6d6cf5c79ac7af0694ff22e2b5de1477b",
	"pooler":        "ghcr.io/hakopod/managed-supabase-pooler@sha256:91930deeb066e948a287f74e680388e7e54d397005f430c2e3e71f36799d07ec",
	"postgres-meta": "docker.io/supabase/postgres-meta@sha256:09b00cdd401f830cc8db5c7da14468e99d04a63900371b1ec06463674ac4877e",
	"realtime":      "docker.io/supabase/realtime@sha256:c1d078d929608f3eb4d441e30317bbdccc1bfcc1169efa3d1a26d4252417cc76",
	"rest":          "docker.io/postgrest/postgrest@sha256:aa7e96af2d01219a09bc00c75de28171b1f9fda17ea455a931e4fb6d317089e0",
	"storage":       "docker.io/supabase/storage-api@sha256:63da55733ce9d7592d860739acb94f0b6189d880b7565d247ee5b195be854db1",
	"studio":        "docker.io/supabase/studio@sha256:0f797270d236090c79fd26a97a1e5d1cf7ec5f36e93b9b4c3c453e1cb1aeadab",
}

// SupabaseReleaseQualified is changed only in a release whose exact source,
// images and native evidence have passed the committed release verifier.
func SupabaseReleaseQualified() bool { return false }

func SupabaseReleaseImages() map[string]string {
	images := make(map[string]string, len(supabaseReleaseImages))
	for name, image := range supabaseReleaseImages {
		images[name] = image
	}
	return images
}

func SupabaseReleaseImagesMatch(images map[string]string) bool {
	return len(images) == len(supabaseReleaseImages) && SupabaseImageInventorySHA256(images) == SupabaseReleaseImageInventorySHA256
}

func SupabaseImageInventorySHA256(images map[string]string) string {
	items := make([]string, 0, len(supabaseComponents))
	for _, name := range supabaseComponents {
		items = append(items, name+"="+images[name])
	}
	digest := sha256.Sum256([]byte(strings.Join(items, "\n")))
	return hex.EncodeToString(digest[:])
}
