package advisory

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	maxFeedBytes    = 1 << 20
	maxAdvisories   = 500
	maxTargets      = 32
	maxTitle        = 200
	maxMessage      = 2000
	maxClockSkew    = 5 * time.Minute
	maxFeedValidity = 31 * 24 * time.Hour
)

//go:embed signing-public-key.txt
var embeddedPublicKey string

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// PublicKey returns the embedded Hakopod advisory signing key. It panics on a
// malformed embedded key, which is a build defect.
func PublicKey() ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(embeddedPublicKey))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic("advisory: malformed embedded signing-public-key.txt")
	}
	return ed25519.PublicKey(raw)
}

// Verify checks the signature and contents of a signed feed. An expired feed is
// not an error; the caller compares ExpiresAt with now and marks it stale.
func Verify(signed []byte, pub ed25519.PublicKey, now time.Time, minRevision int64) (Feed, error) {
	if len(signed) > maxFeedBytes {
		return Feed{}, errors.New("advisory feed exceeds 1 MiB")
	}
	var sf SignedFeed
	if err := json.Unmarshal(signed, &sf); err != nil {
		return Feed{}, fmt.Errorf("decode signed feed: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sf.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Feed{}, errors.New("advisory feed signature is malformed")
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, []byte(SignatureDomain+sf.Payload), sig) {
		return Feed{}, errors.New("advisory feed signature does not verify")
	}
	dec := json.NewDecoder(strings.NewReader(sf.Payload))
	dec.DisallowUnknownFields()
	var feed Feed
	if err := dec.Decode(&feed); err != nil {
		return Feed{}, fmt.Errorf("decode advisory feed: %w", err)
	}
	if dec.More() {
		return Feed{}, errors.New("advisory feed has trailing data")
	}
	if feed.Format != FeedFormat {
		return Feed{}, fmt.Errorf("advisory feed format %d is not supported", feed.Format)
	}
	if feed.Revision < minRevision {
		return Feed{}, fmt.Errorf("advisory feed revision %d is older than %d", feed.Revision, minRevision)
	}
	if feed.IssuedAt.After(now.Add(maxClockSkew)) {
		return Feed{}, errors.New("advisory feed is issued in the future")
	}
	if validity := feed.ExpiresAt.Sub(feed.IssuedAt); validity <= 0 || validity > maxFeedValidity {
		return Feed{}, errors.New("advisory feed validity window is invalid")
	}
	if len(feed.Advisories) > maxAdvisories {
		return Feed{}, fmt.Errorf("advisory feed has more than %d advisories", maxAdvisories)
	}
	seen := map[string]bool{}
	for i, a := range feed.Advisories {
		if a.ID == "" || seen[a.ID] {
			return Feed{}, fmt.Errorf("advisory %d: id is empty or duplicated", i)
		}
		seen[a.ID] = true
		if err := validateAdvisory(a); err != nil {
			return Feed{}, fmt.Errorf("advisory %s: %w", a.ID, err)
		}
	}
	return feed, nil
}

func validateAdvisory(a Advisory) error {
	if a.Revision < 1 {
		return errors.New("revision must be at least 1")
	}
	switch a.Severity {
	case Critical, High, Moderate, Low:
	default:
		return fmt.Errorf("unknown severity %q", a.Severity)
	}
	if a.Action != ActionUpdate && a.Action != ActionManual {
		return fmt.Errorf("unknown action %q", a.Action)
	}
	if a.Source != SourceHakopod {
		return errors.New(`source must be "hakopod"`)
	}
	if !strings.HasPrefix(a.URL, "https://") || len(a.URL) == len("https://") {
		return errors.New("url must be https")
	}
	if len([]rune(a.Title)) > maxTitle || len([]rune(a.Message)) > maxMessage {
		return errors.New("title or message is too long")
	}
	if len(a.Aliases) > 20 {
		return errors.New("more than 20 aliases")
	}
	for _, alias := range a.Aliases {
		if !aliasPattern.MatchString(alias) {
			return fmt.Errorf("invalid alias %q", alias)
		}
	}
	if len(a.Targets) < 1 || len(a.Targets) > maxTargets {
		return fmt.Errorf("must have 1 to %d targets", maxTargets)
	}
	fixable := true
	for _, t := range a.Targets {
		switch {
		case (t.Image == nil) == (t.Runtime == nil):
			return errors.New("target must have exactly one of image or runtime")
		case t.Image != nil:
			if t.Image.Repository == "" {
				return errors.New("image repository is empty")
			}
			for _, list := range [][]string{t.Image.AffectedDigests, t.Image.FixedDigests} {
				for _, d := range list {
					if !digestPattern.MatchString(d) {
						return fmt.Errorf("invalid digest %q", d)
					}
				}
			}
			fixable = fixable && len(t.Image.FixedDigests) > 0
		default:
			if _, ok := RuntimeEnv[t.Runtime.Runtime]; !ok {
				return fmt.Errorf("unknown runtime %q", t.Runtime.Runtime)
			}
			if len(t.Runtime.Ranges) == 0 {
				return errors.New("runtime target has no ranges")
			}
			for _, r := range t.Runtime.Ranges {
				if r.Introduced == "" {
					return errors.New("range introduced is required")
				}
				fixable = fixable && r.Fixed != ""
			}
		}
	}
	if a.Action == ActionUpdate && !fixable {
		return errors.New("update advisory must name a fix on every target")
	}
	return nil
}
