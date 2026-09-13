//go:build hakopod_cloud && !hakopod_selfhosted

package main

// Cloud builds still require managed-cloud mode and the operator signup switch.
const cloudSignupAvailable = true
