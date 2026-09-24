package spec

import (
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
)

// VolumeResize replaces one physical claim without modifying the original.
// A private named volume lets both claims coexist through verified cutover.
type VolumeResize struct {
	Claim       string   `json:"claim"`
	TargetClaim string   `json:"target_claim"`
	TargetName  string   `json:"target_name"`
	OldGiB      int64    `json:"old_gib"`
	SizeGiB     int64    `json:"size_gib"`
	Services    []string `json:"services"`
	User        int64    `json:"-"`
	Group       int64    `json:"-"`
	FSGroup     int64    `json:"-"`
}

func HasVolumeClaim(a Application, claim string) bool {
	for name := range a.Volumes {
		if claim == "hakopod-volume-"+name {
			return true
		}
	}
	for name, s := range a.Services {
		if s.Volume != nil && claim == name+"-data" {
			return true
		}
	}
	return false
}

func ResizeVolume(original Application, claim string, size int64, operation string) (Application, VolumeResize, error) {
	next, err := Normalize(original)
	r := VolumeResize{Claim: claim, SizeGiB: size, Services: []string{}}
	if err != nil {
		return next, r, err
	}
	if size < 1 || size > 200 {
		return next, r, fmt.Errorf("choose a volume size between 1 and 200 GiB")
	}
	sum := sha256.Sum256([]byte(operation + ":" + claim))
	r.TargetName = fmt.Sprintf("resize-%x", sum[:10])
	r.TargetClaim = "hakopod-volume-" + r.TargetName
	var volume NamedVolume
	var named string
	for name, v := range next.Volumes {
		if claim == "hakopod-volume-"+name {
			volume = v
			named = name
		}
	}
	for name, svc := range next.Services {
		if svc.Job != nil {
			return next, r, fmt.Errorf("applications with deployment or scheduled jobs require an operator-managed volume migration")
		}
		affected := false
		if svc.Volume != nil && claim == name+"-data" {
			v := svc.Volume
			volume = NamedVolume{SizeGiB: v.SizeGiB, StorageClass: v.StorageClass, AccessMode: "ReadWriteOnce"}
			svc.Mounts = append(svc.Mounts, Mount{Volume: r.TargetName, MountPath: v.MountPath, SubPath: "data"})
			svc.Volume = nil
			affected = true
		}
		for i, m := range svc.Mounts {
			if named != "" && m.Volume == named {
				m.Volume = r.TargetName
				m.SubPath = path.Join("data", m.SubPath)
				svc.Mounts[i] = m
				affected = true
			}
		}
		if affected {
			if svc.Job != nil || svc.Autoscaling != nil || svc.Replicas != 1 || svc.Serverless != nil {
				return next, r, fmt.Errorf("resize requires single-replica services without autoscaling or jobs")
			}
			user, group, fs := svc.RunAsUser, svc.RunAsGroup, svc.FSGroup
			if user == 0 {
				user = 10001
			}
			if group == 0 {
				group = user
			}
			if fs == 0 {
				fs = user
			}
			if len(r.Services) > 0 && (r.User != user || r.Group != group || r.FSGroup != fs) {
				return next, r, fmt.Errorf("shared-volume services must use the same filesystem identity for migration")
			}
			r.User = user
			r.Group = group
			r.FSGroup = fs
			r.Services = append(r.Services, name)
			next.Services[name] = svc
		}
	}
	if volume.SizeGiB == 0 || len(r.Services) == 0 {
		return next, r, fmt.Errorf("choose an attached persistent volume")
	}
	if size == volume.SizeGiB {
		return next, r, fmt.Errorf("choose a different volume size")
	}
	r.OldGiB = volume.SizeGiB
	volume.SizeGiB = size
	if next.Volumes == nil {
		next.Volumes = map[string]NamedVolume{}
	}
	if named != "" {
		delete(next.Volumes, named)
	}
	next.Volumes[r.TargetName] = volume
	sort.Strings(r.Services)
	next, err = Normalize(next)
	return next, r, err
}
