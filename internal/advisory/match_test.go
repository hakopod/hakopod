package advisory

import (
	"reflect"
	"testing"
)

const (
	dBad   = "sha256:aaaa"
	dFixed = "sha256:ffff"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"22.3.0", "22.12.1", -1}, {"v1.22.5", "1.22.5", 0}, {"3.12.4-slim", "3.12.10", -1},
		{"3.12", "3.12.0", 0}, {"1.0.0", "0", 1}, {"21.0.4+7", "21.0.4", 0}, {"garbage", "0", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNormalizeRepositoryAndImageTag(t *testing.T) {
	for _, c := range []struct{ in, repo, tag string }{
		{"node", "docker.io/library/node", "latest"},
		{"user/app", "docker.io/user/app", "latest"},
		{"ghcr.io/a/b:tag@sha256:abc", "ghcr.io/a/b", "tag"},
		{"localhost:5000/x:1", "localhost:5000/x", "1"},
		{"GHCR.IO/a/b", "ghcr.io/a/b", "latest"},
		{"docker.io/node:22", "docker.io/library/node", "22"},
		{"node@sha256:abc", "docker.io/library/node", ""},
	} {
		if got := NormalizeRepository(c.in); got != c.repo {
			t.Errorf("NormalizeRepository(%q)=%q want %q", c.in, got, c.repo)
		}
		if got := ImageTag(c.in); got != c.tag {
			t.Errorf("ImageTag(%q)=%q want %q", c.in, got, c.tag)
		}
	}
}

func imageAdv(id string, sev Severity) Advisory {
	return Advisory{ID: id, Revision: 2, Source: SourceHakopod, Severity: sev, Action: ActionUpdate, Targets: []Target{{Image: &ImageTarget{
		Repository: "node", AffectedDigests: []string{dBad}, AffectedTags: []string{"22"}, FixedDigests: []string{dFixed},
	}}}}
}

func runtimeAdv(id string, ranges ...Range) Advisory {
	return Advisory{ID: id, Source: SourceOSV, Severity: High, Action: ActionUpdate, Targets: []Target{{Runtime: &RuntimeTarget{Runtime: "node", Ranges: ranges}}}}
}

func TestMatch(t *testing.T) {
	img := func(image, digest string, mutable bool) ServiceEvidence {
		return ServiceEvidence{Service: "web", Kind: KindImage, Image: image, Digest: digest, Mutable: mutable}
	}
	type want struct {
		conf     Confidence
		evidence string
		fix      FixKind
		reason   string
		auto     bool
	}
	for _, c := range []struct {
		name string
		adv  Advisory
		svc  ServiceEvidence
		want *want
	}{
		{"confirmed digest", imageAdv("A", High), img("node:22", dBad, true), &want{Confirmed, "docker.io/library/node@" + dBad, FixReresolve, "", true}},
		{"fixed digest skipped", imageAdv("A", High), img("node:22", dFixed, true), nil},
		{"possible tag", imageAdv("A", High), img("docker.io/library/node:22", "sha256:other", true), &want{Possible, "docker.io/library/node:22", FixReresolve, "", false}},
		{"other repo", imageAdv("A", High), img("nodejs:22", dBad, true), nil},
		{"pinned by digest", imageAdv("A", High), img("node@"+dBad, dBad, false), &want{Confirmed, "docker.io/library/node@" + dBad, FixManual, "Image is pinned by digest; change the pinned digest.", false}},
		{"osv not auto", func() Advisory { a := imageAdv("A", High); a.Source = SourceOSV; return a }(), img("node:22", dBad, true), &want{Confirmed, "docker.io/library/node@" + dBad, FixReresolve, "", false}},
		{"manual action", func() Advisory { a := imageAdv("A", High); a.Action = ActionManual; a.Message = "Change it."; return a }(), img("node:22", "sha256:x", true), &want{Possible, "docker.io/library/node:22", FixManual, "Change it.", false}},
		{"manual action not auto", func() Advisory { a := imageAdv("A", High); a.Action = ActionManual; return a }(), img("node:22", dBad, true), &want{Confirmed, "docker.io/library/node@" + dBad, FixManual, "", false}},
		{"source rebuild", imageAdv("A", High), ServiceEvidence{Service: "web", Kind: KindSource, Image: "registry.example/app:abc", Digest: dBad, BaseImage: "node:22"}, &want{Possible, "docker.io/library/node:22", FixRebuild, "", false}},
		{"source base pinned", imageAdv("A", High), ServiceEvidence{Service: "web", Kind: KindSource, Image: "registry.example/app:abc", BaseImage: "node:22@" + dBad, BasePinned: true}, &want{Confirmed, "docker.io/library/node@" + dBad, FixManual, "Base image is pinned by Hakopod; update Hakopod, then rebuild.", false}},
		{"runtime in range", runtimeAdv("R", Range{Introduced: "22.0.0", Fixed: "22.12.1"}), ServiceEvidence{Service: "web", Kind: KindImage, Mutable: true, Runtimes: map[string]string{"node": "22.3.0"}}, &want{Confirmed, "node 22.3.0", FixReresolve, "", false}},
		{"runtime fixed", runtimeAdv("R", Range{Introduced: "22.0.0", Fixed: "22.12.1"}), ServiceEvidence{Service: "web", Kind: KindImage, Runtimes: map[string]string{"node": "22.12.1"}}, nil},
		{"runtime below", runtimeAdv("R", Range{Introduced: "22.0.0", Fixed: "22.12.1"}), ServiceEvidence{Service: "web", Kind: KindImage, Runtimes: map[string]string{"node": "20.1.0"}}, nil},
		{"runtime unfixed", runtimeAdv("R", Range{Introduced: "0"}), ServiceEvidence{Service: "web", Kind: KindImage, Mutable: true, Runtimes: map[string]string{"node": "v1.0.0"}}, &want{Confirmed, "node 1.0.0", FixReresolve, "", false}},
		{"last affected inclusive", runtimeAdv("R", Range{Introduced: "22.0.0", LastAffected: "22.3.0"}), ServiceEvidence{Service: "web", Kind: KindImage, Mutable: true, Runtimes: map[string]string{"node": "22.3.0"}}, &want{Confirmed, "node 22.3.0", FixReresolve, "", false}},
		{"last affected above", runtimeAdv("R", Range{Introduced: "22.0.0", LastAffected: "22.3.0"}), ServiceEvidence{Service: "web", Kind: KindImage, Runtimes: map[string]string{"node": "22.3.1"}}, nil},
		{"normalised evidence", Advisory{ID: "J", Severity: High, Action: ActionUpdate, Targets: []Target{{Runtime: &RuntimeTarget{Runtime: "java", Ranges: []Range{{Introduced: "21.0.0", Fixed: "21.0.3"}}}}}}, ServiceEvidence{Service: "web", Kind: KindImage, Mutable: true, Runtimes: map[string]string{"java": "jdk-21.0.2+13"}}, &want{Confirmed, "java 21.0.2", FixReresolve, "", false}},
		{"runtime unknown", runtimeAdv("R", Range{Introduced: "0"}), ServiceEvidence{Service: "web", Kind: KindImage, Unknown: true}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Match([]Advisory{c.adv}, Inventory{Services: []ServiceEvidence{c.svc}})
			if c.want == nil {
				if len(got) != 0 {
					t.Fatalf("unexpected %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want 1 finding, got %+v", got)
			}
			f := got[0]
			if f.Confidence != c.want.conf || f.Evidence != c.want.evidence || f.Fix != c.want.fix || f.FixReason != c.want.reason || f.AutoEligible != c.want.auto {
				t.Fatalf("got %+v want %+v", f, *c.want)
			}
			if f.AutoEligible && !reflect.DeepEqual(f.FixedDigests, []string{dFixed}) {
				t.Fatalf("fixed digests %v", f.FixedDigests)
			}
		})
	}
}

func TestMatchMerge(t *testing.T) {
	adv := imageAdv("A", High)
	adv.Targets = append(adv.Targets, adv.Targets[0], Target{Runtime: &RuntimeTarget{Runtime: "node", Ranges: []Range{{Introduced: "0"}}}})
	adv.Targets[0].Image = &ImageTarget{Repository: "docker.io/library/node", AffectedTags: []string{"22"}}
	svc := ServiceEvidence{Service: "web", Kind: KindImage, Image: "node:22", Digest: dBad, Mutable: true, Runtimes: map[string]string{"node": "22.3.0"}}
	got := Match([]Advisory{adv}, Inventory{Services: []ServiceEvidence{svc}})
	if len(got) != 1 || got[0].Confidence != Confirmed || got[0].Evidence != "docker.io/library/node:22, docker.io/library/node@"+dBad+", node 22.3.0" || !got[0].AutoEligible {
		t.Fatalf("merge: %+v", got)
	}
}

func TestMatchOrder(t *testing.T) {
	svc := func(name string) ServiceEvidence {
		return ServiceEvidence{Service: name, Kind: KindImage, Image: "node:22", Digest: dBad, Mutable: true}
	}
	got := Match([]Advisory{imageAdv("B", Low), imageAdv("Z", Critical), imageAdv("A", Low), imageAdv("M", Moderate)}, Inventory{Services: []ServiceEvidence{svc("worker"), svc("api")}})
	var order []string
	for _, f := range got {
		order = append(order, f.AdvisoryID+"/"+f.Service)
	}
	want := []string{"Z/api", "Z/worker", "M/api", "M/worker", "A/api", "A/worker", "B/api", "B/worker"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order %v", order)
	}
}

func TestNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"jdk-21.0.2+13": "21.0.2", "3.12.4-slim": "3.12.4", "v1.22.5": "1.22.5", "22.3.0": "22.3.0",
		"1.23.4": "1.23.4", "": "", "latest": "", "v": "", "jdk-": "", "-rc1": "", "8.3.13": "8.3.13",
	} {
		if got := NormalizeVersion("any", in); got != want {
			t.Errorf("NormalizeVersion(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMatchAliases(t *testing.T) {
	adv := imageAdv("A", High)
	adv.Aliases = []string{"CVE-1"}
	got := Match([]Advisory{adv}, Inventory{Services: []ServiceEvidence{{Service: "web", Kind: KindImage, Image: "node:22", Digest: dBad, Mutable: true}}})
	if len(got) != 1 || !reflect.DeepEqual(got[0].Aliases, []string{"CVE-1"}) {
		t.Fatalf("aliases: %+v", got)
	}
}

func TestMerge(t *testing.T) {
	primary := []Finding{{AdvisoryID: "HAKO-1", Aliases: []string{"CVE-2026-1"}, Severity: Moderate, Service: "web"}}
	secondary := []Finding{
		{AdvisoryID: "GHSA-x", Aliases: []string{"cve-2026-1"}, Severity: High, Service: "web"},    // alias dup: dropped
		{AdvisoryID: "hako-1", Severity: High, Service: "web"},                                     // id dup: dropped
		{AdvisoryID: "GHSA-x", Aliases: []string{"CVE-2026-1"}, Severity: High, Service: "worker"}, // other service: kept
		{AdvisoryID: "GHSA-y", Severity: Critical, Service: "web"},                                 // unrelated: kept
	}
	var got []string
	for _, f := range Merge(primary, secondary) {
		got = append(got, f.AdvisoryID+"/"+f.Service)
	}
	if want := []string{"GHSA-y/web", "GHSA-x/worker", "HAKO-1/web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merge %v", got)
	}
}

func TestMergeKeepsStrongerSecondary(t *testing.T) {
	primary := []Finding{{AdvisoryID: "HAKO-1", Aliases: []string{"CVE-1"}, Confidence: Possible, Service: "web"}}
	secondary := []Finding{
		{AdvisoryID: "GHSA-a", Aliases: []string{"CVE-1"}, Confidence: Confirmed, Service: "web"}, // stronger: kept
		{AdvisoryID: "GHSA-b", Aliases: []string{"CVE-1"}, Confidence: Possible, Service: "web"},  // equal: dropped
	}
	if got := Merge(primary, secondary); len(got) != 2 || got[0].AdvisoryID != "GHSA-a" {
		t.Fatalf("merge %+v", got)
	}
}
