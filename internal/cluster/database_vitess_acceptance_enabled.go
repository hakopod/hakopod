//go:build hakopod_native_acceptance && linux

package cluster

// vitessNativeAcceptance is available only in the separate Linux development
// acceptance build. Shipping builds never compile this value.
const vitessNativeAcceptance = true
