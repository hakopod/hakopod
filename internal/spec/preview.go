package spec

import "fmt"

// Preview limits apply to every revision, including updates through the normal
// application API. Native secrets resolve against the new application name.
func ValidatePreview(a Application) error {
	a = RuntimeEnvironment(a)
	if len(a.Services) > 4 {
		return fmt.Errorf("previews support at most four small services")
	}
	if len(a.Domains) > 0 {
		return fmt.Errorf("previews use their generated hostname; remove custom domains")
	}
	for _, n := range a.Networks {
		if n.VirtualNetwork != "" || n.Segment != "" {
			return fmt.Errorf("previews cannot join shared virtual networks")
		}
	}
	var disk int64
	for _, v := range a.Volumes {
		disk += v.SizeGiB
	}
	for _, s := range a.Services {
		if s.Actions != nil {
			return fmt.Errorf("Managed Actions pools cannot be copied into previews")
		}
		if s.Size != "small" || s.Replicas > 1 || s.Autoscaling != nil || s.GPU != nil {
			return fmt.Errorf("previews require the small profile, one replica and no autoscaling or GPU")
		}
		// A preview is a separate scope, so it cannot inherit a grant that names
		// the original application and service.
		if len(s.PublicTCP) > 0 || len(s.CertificateMounts) > 0 || s.AWSIdentity != "" || s.ContainerDaemon != "" || s.TLS != nil {
			return fmt.Errorf("previews do not expose public TCP or inherit certificates, AWS identities or container daemon bindings")
		}
		if s.Job != nil && s.Job.Schedule != nil && !s.Suspended {
			return fmt.Errorf("scheduled jobs must stay paused in previews")
		}
		if s.Volume != nil {
			disk += s.Volume.SizeGiB
		}
		for _, ref := range SecretReferences(s) {
			if ref.Provider != "" {
				return fmt.Errorf("previews require their own native secrets; external secret providers are not inherited")
			}
		}
	}
	if disk > 5 {
		return fmt.Errorf("preview storage is limited to 5 GiB and deleted at expiry")
	}
	return nil
}
