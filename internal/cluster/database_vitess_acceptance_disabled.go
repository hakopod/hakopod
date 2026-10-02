//go:build !hakopod_native_acceptance || !linux

package cluster

// vitessNativeAcceptance keeps normal and non-Linux builds fail-closed.
const vitessNativeAcceptance = false
