package advisory

import (
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Match returns one finding per (advisory, service), strongest confidence
// first, sorted by severity, advisory id and service. It is pure.
func Match(advisories []Advisory, inv Inventory) []Finding {
	var findings []Finding
	for _, adv := range advisories {
		for _, svc := range inv.Services {
			var f *Finding
			add := func(conf Confidence, evidence string, image *ImageTarget) {
				if f == nil {
					f = &Finding{AdvisoryID: adv.ID, AdvisoryRevision: adv.Revision, Source: adv.Source, Severity: adv.Severity, Title: adv.Title, URL: adv.URL, Aliases: adv.Aliases, Service: svc.Service, Confidence: conf, Evidence: evidence}
				} else {
					if conf == Confirmed {
						f.Confidence = Confirmed
					}
					if !slices.Contains(strings.Split(f.Evidence, ", "), evidence) {
						f.Evidence += ", " + evidence
					}
				}
				if image != nil && conf == Confirmed && adv.Source == SourceHakopod && adv.Action == ActionUpdate && svc.Kind == KindImage && svc.Mutable && len(image.FixedDigests) > 0 {
					f.AutoEligible = true
					f.FixedDigests = image.FixedDigests
				}
			}
			for _, t := range adv.Targets {
				switch {
				case t.Image != nil:
					matchImage(t.Image, svc, add)
				case t.Runtime != nil:
					v := NormalizeVersion(t.Runtime.Runtime, svc.Runtimes[t.Runtime.Runtime])
					if v != "" && inRanges(v, t.Runtime.Ranges) {
						add(Confirmed, t.Runtime.Runtime+" "+v, nil)
					}
				}
			}
			if f != nil {
				f.Fix, f.FixReason = fixFor(adv, svc)
				findings = append(findings, *f)
			}
		}
	}
	sortFindings(findings)
	return findings
}

func sortFindings(findings []Finding) {
	rank := map[Severity]int{Critical: 0, High: 1, Moderate: 2, Low: 3}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		ra, okA := rank[a.Severity]
		rb, okB := rank[b.Severity]
		if !okA {
			ra = 4
		}
		if !okB {
			rb = 4
		}
		if ra != rb {
			return ra < rb
		}
		if a.AdvisoryID != b.AdvisoryID {
			return a.AdvisoryID < b.AdvisoryID
		}
		return a.Service < b.Service
	})
}

// Merge returns primary plus each secondary finding that does not share both a
// service and an identifier (advisory id or alias, case-insensitive) with a
// primary finding, so an OSV duplicate of a Hakopod advisory is dropped. A
// confirmed secondary is kept when the matching primary is only possible.
func Merge(primary, secondary []Finding) []Finding {
	seen := make(map[string]Confidence)
	for _, f := range primary {
		for _, id := range append([]string{f.AdvisoryID}, f.Aliases...) {
			if key := f.Service + "\x00" + strings.ToLower(id); seen[key] != Confirmed {
				seen[key] = f.Confidence
			}
		}
	}
	out := append([]Finding(nil), primary...)
next:
	for _, f := range secondary {
		for _, id := range append([]string{f.AdvisoryID}, f.Aliases...) {
			// Drop only when the primary is at least as strong.
			if c, ok := seen[f.Service+"\x00"+strings.ToLower(id)]; ok && (c == Confirmed || f.Confidence != Confirmed) {
				continue next
			}
		}
		out = append(out, f)
	}
	sortFindings(out)
	return out
}

func matchImage(t *ImageTarget, svc ServiceEvidence, add func(Confidence, string, *ImageTarget)) {
	image := svc.Image
	if svc.Kind == KindSource {
		image = svc.BaseImage
	}
	repo := NormalizeRepository(image)
	if image == "" || repo != NormalizeRepository(t.Repository) {
		return
	}
	digest := svc.Digest
	if svc.Kind == KindSource {
		// The running digest is the built image; only the FROM digest says
		// anything about the base.
		digest = ""
		if _, d, ok := strings.Cut(image, "@"); ok {
			digest = d
		}
	}
	if digest != "" && slices.Contains(t.FixedDigests, digest) {
		return
	}
	if digest != "" && slices.Contains(t.AffectedDigests, digest) {
		add(Confirmed, repo+"@"+digest, t)
		return
	}
	if tag := ImageTag(image); tag != "" && slices.Contains(t.AffectedTags, tag) {
		add(Possible, repo+":"+tag, t)
	}
}

func inRanges(v string, ranges []Range) bool {
	for _, r := range ranges {
		if CompareVersions(v, r.Introduced) < 0 {
			continue
		}
		switch {
		case r.Fixed != "" && CompareVersions(v, r.Fixed) < 0,
			r.LastAffected != "" && CompareVersions(v, r.LastAffected) <= 0,
			r.Fixed == "" && r.LastAffected == "":
			return true
		}
	}
	return false
}

func fixFor(adv Advisory, svc ServiceEvidence) (FixKind, string) {
	switch {
	case adv.Action == ActionManual:
		return FixManual, adv.Message
	case svc.Kind == KindSource && svc.BasePinned:
		return FixManual, "Base image is pinned by Hakopod; update Hakopod, then rebuild."
	case svc.Kind == KindSource:
		return FixRebuild, ""
	case !svc.Mutable:
		return FixManual, "Image is pinned by digest; change the pinned digest."
	default:
		return FixReresolve, ""
	}
}

// NormalizeVersion reduces an image's runtime version variable to dotted
// digits: "jdk-21.0.2+13" is "21.0.2", "v1.22.5" is "1.22.5", "3.12.4-slim" is
// "3.12.4". It returns "" when nothing numeric remains. runtime is accepted
// for runtime-specific rules; none are needed yet.
func NormalizeVersion(runtime, raw string) string {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "jdk-"), "v")
	if i := strings.IndexAny(v, "+-"); i >= 0 {
		v = v[:i]
	}
	v = strings.Trim(v, ".")
	if v == "" || strings.Trim(v, "0123456789.") != "" || strings.Contains(v, "..") {
		return ""
	}
	return v
}

// CompareVersions compares dotted numeric versions and returns -1, 0 or 1. A
// leading "v" is ignored, each segment is read up to its first non-digit
// ("12-alpine" is 12), missing or non-numeric segments count as 0. So "0"
// sorts at or below every version, and garbage compares equal to "0".
func CompareVersions(a, b string) int {
	as, bs := versionParts(a), versionParts(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var parts []int
	for _, seg := range strings.Split(v, ".") {
		end := 0
		for end < len(seg) && seg[end] >= '0' && seg[end] <= '9' {
			end++
		}
		n, _ := strconv.Atoi(seg[:end])
		parts = append(parts, n)
		if end < len(seg) {
			break // suffix such as "-slim" ends the version
		}
	}
	return parts
}

// splitImage returns the name (registry/repository), tag and digest of image.
func splitImage(image string) (name, tag, digest string) {
	name, digest, _ = strings.Cut(strings.TrimSpace(image), "@")
	if colon := strings.LastIndexByte(name, ':'); colon > strings.LastIndexByte(name, '/') {
		name, tag = name[:colon], name[colon+1:]
	}
	return name, tag, digest
}

// NormalizeRepository returns registry/repository without tag or digest, with
// Docker Hub defaults applied: "node" is "docker.io/library/node".
func NormalizeRepository(image string) string {
	name, _, _ := splitImage(image)
	if name == "" {
		return ""
	}
	first, rest, hasSlash := strings.Cut(name, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		first = strings.ToLower(first)
		if first == "index.docker.io" || first == "registry-1.docker.io" {
			first = "docker.io"
		}
		if first == "docker.io" && !strings.Contains(rest, "/") {
			rest = "library/" + rest
		}
		return first + "/" + rest
	}
	if !hasSlash {
		return "docker.io/library/" + name
	}
	return "docker.io/" + name
}

// ImageTag returns the configured tag: "latest" when none is given, "" when
// the image is referenced only by digest.
func ImageTag(image string) string {
	_, tag, digest := splitImage(image)
	if tag == "" && digest == "" {
		return "latest"
	}
	return tag
}
