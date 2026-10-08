package cluster

import corev1 "k8s.io/api/core/v1"

func podReferencesRetiredSecret(p corev1.PodSpec, name string) bool {
	container := func(env []corev1.EnvVar, from []corev1.EnvFromSource) bool {
		for _, e := range env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
				return true
			}
		}
		for _, e := range from {
			if e.SecretRef != nil && e.SecretRef.Name == name {
				return true
			}
		}
		return false
	}
	for _, c := range p.Containers {
		if container(c.Env, c.EnvFrom) {
			return true
		}
	}
	for _, c := range p.InitContainers {
		if container(c.Env, c.EnvFrom) {
			return true
		}
	}
	for _, c := range p.EphemeralContainers {
		if container(c.Env, c.EnvFrom) {
			return true
		}
	}
	for _, ref := range p.ImagePullSecrets {
		if ref.Name == name {
			return true
		}
	}
	for _, v := range p.Volumes {
		if v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
		if v.Projected != nil {
			for _, source := range v.Projected.Sources {
				if source.Secret != nil && source.Secret.Name == name {
					return true
				}
			}
		}
		if v.CSI != nil && v.CSI.NodePublishSecretRef != nil && v.CSI.NodePublishSecretRef.Name == name {
			return true
		}
		if v.AzureFile != nil && v.AzureFile.SecretName == name {
			return true
		}
		if v.CephFS != nil && v.CephFS.SecretRef != nil && v.CephFS.SecretRef.Name == name {
			return true
		}
		if v.RBD != nil && v.RBD.SecretRef != nil && v.RBD.SecretRef.Name == name {
			return true
		}
		if v.ISCSI != nil && v.ISCSI.SecretRef != nil && v.ISCSI.SecretRef.Name == name {
			return true
		}
		if v.Cinder != nil && v.Cinder.SecretRef != nil && v.Cinder.SecretRef.Name == name {
			return true
		}
		if v.FlexVolume != nil && v.FlexVolume.SecretRef != nil && v.FlexVolume.SecretRef.Name == name {
			return true
		}
		if v.ScaleIO != nil && v.ScaleIO.SecretRef != nil && v.ScaleIO.SecretRef.Name == name {
			return true
		}
		if v.StorageOS != nil && v.StorageOS.SecretRef != nil && v.StorageOS.SecretRef.Name == name {
			return true
		}
	}
	return false
}
