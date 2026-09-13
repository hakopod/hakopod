//go:build !hakopod_cloud || hakopod_selfhosted

package main

// Public release binaries cannot enable public enrollment through configuration.
const cloudSignupAvailable = false
