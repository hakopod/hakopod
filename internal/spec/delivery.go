package spec

// HasDeliveryCapabilities identifies specifications that need operator and
// cluster validation beyond the portable TOML schema.
func HasDeliveryCapabilities(app Application) bool {
	for _, svc := range app.Services {
		if len(svc.PublicTCP) > 0 || len(svc.CertificateMounts) > 0 || svc.AWSIdentity != "" || NeedsReadinessHelper(svc) {
			return true
		}
	}
	return false
}
