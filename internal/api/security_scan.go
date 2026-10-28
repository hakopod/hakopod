package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
	"github.com/hakopod/hakopod/internal/framework"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// advisoryOwner identifies this process in feed and scanner leases.
var advisoryOwner = store.NewID()

var advisoryFeedHTTP = &http.Client{Timeout: 30 * time.Second}

const (
	securityNotifyCap  = 5
	securityOSVMaxAge  = 24 * time.Hour
	securityScanBatch  = 25
	securityScanBudget = 100 * time.Second
)

// RunAdvisories refreshes the signed feed and evaluates due applications. A
// database lease coordinates API replicas for each step.
func (s *Server) RunAdvisories(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.refreshAdvisoryFeed(ctx)
		s.scanAdvisories(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refreshAdvisoryFeed(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	settings, _, err := s.Store.SecuritySettings(ctx)
	if err != nil || !settings.FeedEnabled {
		return
	}
	if ok, err := s.Store.ClaimFeed(ctx, advisoryOwner, time.Minute); err != nil || !ok {
		return
	}
	st, err := s.Store.FeedState(ctx)
	if err != nil {
		return
	}
	url := os.Getenv("HAKOPOD_ADVISORY_FEED_URL")
	if url == "" {
		url = advisory.DefaultFeedURL
	}
	now := time.Now().UTC()
	st.CheckedAt = &now
	res, fetchErr := advisory.Fetch(ctx, advisoryFeedHTTP, url, st.ETag)
	var verifyErr error
	if fetchErr == nil && !res.NotModified {
		var feed advisory.Feed
		if feed, verifyErr = advisory.Verify(res.Body, advisory.PublicKey(), now, st.Revision); verifyErr == nil {
			st.Body, st.ETag, st.Revision = res.Body, res.ETag, feed.Revision
			st.IssuedAt, st.ExpiresAt = &feed.IssuedAt, &feed.ExpiresAt
		}
	}
	if fetchErr == nil && verifyErr == nil {
		st.Failures, st.Error, st.NextCheckAt = 0, "", now.Add(time.Hour)
	} else {
		// The previous verified body stays in place. Neither the body nor
		// raw error text (which can carry a URL with a token) is stored.
		st.Failures++
		st.Error = feedErrorClass(fetchErr, verifyErr, len(st.Body) > 0)
		st.NextCheckAt = now.Add(advisory.Backoff(st.Failures, res.RetryAfter))
	}
	_ = s.Store.SaveFeed(ctx, advisoryOwner, st)
}

var feedStatusPattern = regexp.MustCompile(`unexpected status (\d{3})`)

// feedErrorClass reduces a feed failure to fixed text.
func feedErrorClass(fetchErr, verifyErr error, everVerified bool) string {
	if fetchErr != nil {
		m := feedStatusPattern.FindStringSubmatch(fetchErr.Error())
		switch {
		case m == nil:
			return "Could not reach the advisory feed."
		case m[1] == "404" && !everVerified:
			return "The advisory feed has not been published yet."
		}
		return "The advisory feed returned HTTP " + m[1] + "."
	}
	if strings.Contains(verifyErr.Error(), "signature") {
		return "The advisory feed signature does not verify."
	}
	return "The advisory feed is invalid."
}

// securityInputs is what one evaluation reads besides the application.
type securityInputs struct {
	settings store.SecuritySettings
	state    store.FeedState
	feed     *advisory.Feed
}

func (s *Server) loadSecurityInputs(ctx context.Context) (securityInputs, error) {
	var in securityInputs
	var err error
	if in.settings, _, err = s.Store.SecuritySettings(ctx); err != nil {
		return in, err
	}
	if in.state, err = s.Store.FeedState(ctx); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return in, err
	}
	if in.settings.FeedEnabled && len(in.state.Body) > 0 {
		// Expiry is not a verification error; staleness is reported separately.
		if feed, err := advisory.Verify(in.state.Body, advisory.PublicKey(), time.Now().UTC(), 0); err == nil {
			in.feed = &feed
		}
	}
	return in, nil
}

func (s *Server) scanAdvisories(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, securityScanBudget)
	defer cancel()
	if ok, err := s.Store.ClaimAdvisoryScan(ctx, advisoryOwner, 2*time.Minute); err != nil || !ok {
		return
	}
	in, err := s.loadSecurityInputs(ctx)
	if err != nil {
		return
	}
	s.reconcilePatchAttempts(ctx, in)
	feedRevision := int64(0)
	if in.feed != nil {
		feedRevision = in.state.Revision
	}
	candidates, err := s.Store.SecurityCandidates(ctx, feedRevision, securityOSVMaxAge, in.settings.OSVEnabled, securityScanBatch)
	if err != nil {
		return
	}
	rows := make([]*store.ApplicationSecurity, len(candidates))
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if row, err := s.evaluateSecurity(ctx, c, in); err == nil {
				rows[i] = &row
			}
		}()
	}
	wg.Wait()
	for i, row := range rows {
		if row != nil && s.autoPatch(ctx, candidates[i], *row, in) {
			return // at most one automatic apply per scan step
		}
	}
}

func digestOf(image string) string {
	_, digest, _ := strings.Cut(image, "@")
	return digest
}

// buildInventory maps one running deployment to advisory evidence. env reads
// the image configuration environment of a resolved image reference.
func buildInventory(c store.SecurityCandidate, configured, resolved spec.Application, provenance map[string]store.SourceBuild, builds map[string]buildConfig, env func(string) (map[string]string, error)) advisory.Inventory {
	inv := advisory.Inventory{ApplicationID: c.ApplicationID, DeploymentID: c.DeploymentID, Revision: c.Revision, Services: []advisory.ServiceEvidence{}, CheckedAt: time.Now().UTC()}
	for _, name := range spec.Names(configured) {
		image := configured.Services[name].Image
		running := resolved.Services[name].Image
		if image == "" && running == "" {
			continue
		}
		ev := advisory.ServiceEvidence{Service: name, Kind: advisory.KindImage, Image: image, Digest: digestOf(running), Mutable: !strings.Contains(image, "@sha256:")}
		_, built := provenance[name]
		b, configuredBuild := builds[name]
		if built || configuredBuild {
			ev.Kind = advisory.KindSource
		}
		if configuredBuild && b.Mode == "framework" && b.Framework != nil {
			ev.BaseImage, ev.BasePinned = framework.NodeImage, true
			if b.Framework.Runtime == "static" {
				ev.BaseImage = framework.StaticImage
			}
		}
		if running != "" {
			vars, err := env(running)
			if err != nil {
				ev.Reason = "Could not read the image configuration from the registry. Cause: " + registryCause(err) + "."
				inv.Incomplete = append(inv.Incomplete, name+": "+ev.Reason)
			} else {
				for runtime, key := range advisory.RuntimeEnv {
					if v := advisory.NormalizeVersion(runtime, vars[key]); v != "" {
						if ev.Runtimes == nil {
							ev.Runtimes = map[string]string{}
						}
						ev.Runtimes[runtime] = v
					}
				}
			}
		}
		ev.Unknown = len(ev.Runtimes) == 0
		if ev.Unknown && ev.Reason == "" {
			ev.Reason = noRuntimeReason
		}
		inv.Services = append(inv.Services, ev)
	}
	return inv
}

// registryCause reduces a registry error to a fixed class. Raw error text can
// carry registry URLs with credentials, so it never leaves this function.
func registryCause(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "timeout"):
		return "timed out"
	case strings.Contains(text, "unavailable"):
		return "registry access unavailable"
	case strings.Contains(text, "401") || strings.Contains(text, "403") || strings.Contains(text, "unauthorized") || strings.Contains(text, "denied") || strings.Contains(text, "credential"):
		return "access denied"
	case strings.Contains(text, "404") || strings.Contains(text, "not found") || strings.Contains(text, "unknown"):
		return "image not found"
	case strings.Contains(text, "architecture") || strings.Contains(text, "lacks linux"):
		return "no image for the cluster architecture"
	}
	return "registry error"
}

const (
	// noRuntimeReason is normal for images like nginx; it is not a failure.
	noRuntimeReason    = "The image does not record a runtime version."
	osvTruncatedNote   = "OSV.dev returned more results than Hakopod reads; some advisories may be missing."
	osvUnreachableNote = "OSV.dev could not be reached."
	approverPausedNote = "Automatic patching is paused: the approver no longer has deploy access."
)

func (s *Server) applicationBuilds(ctx context.Context, applicationID string) (map[string]buildConfig, error) {
	rows, err := s.Store.Pool.Query(ctx, "SELECT id FROM build_configs WHERE application_id=$1 ORDER BY created_at LIMIT 50", applicationID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := map[string]buildConfig{}
	for _, id := range ids {
		c, err := s.readBuild(ctx, id)
		if err != nil {
			return nil, err
		}
		out[c.Service] = c
	}
	return out, nil
}

// securityRuntimeEnvForTest replaces registry reads in in-process tests only.
var securityRuntimeEnvForTest func(string) (map[string]string, error)

func (s *Server) runtimeEnv(ctx context.Context, project, environment string) func(string) (map[string]string, error) {
	if securityRuntimeEnvForTest != nil {
		return securityRuntimeEnvForTest
	}
	return func(image string) (map[string]string, error) {
		if s.Cluster == nil {
			return nil, errors.New("cluster registry access is unavailable")
		}
		return s.Cluster.ImageRuntimeEnv(ctx, image, project, environment)
	}
}

// osvFindings queries OSV for distinct runtime versions through a 24 hour
// cache. A non-empty note means OSV was truncated or unreachable; results
// are kept but nothing from this pass is cached.
func (s *Server) osvFindings(ctx context.Context, inv advisory.Inventory) (advisories []advisory.Advisory, note string) {
	osv := advisory.NewOSV()
	seen := map[advisory.RuntimeQuery]bool{}
	var misses []advisory.RuntimeQuery
	for _, svc := range inv.Services {
		for runtime, version := range svc.Runtimes {
			q := advisory.RuntimeQuery{Runtime: runtime, Version: version}
			if _, _, ok := osv.Covered(runtime); !ok || seen[q] {
				continue
			}
			seen[q] = true
			body, hit, err := s.Store.OSVCached(ctx, runtime, version, securityOSVMaxAge)
			var cached []advisory.Advisory
			if err == nil && hit && json.Unmarshal(body, &cached) == nil {
				advisories = append(advisories, cached...)
				continue
			}
			misses = append(misses, q)
		}
	}
	fresh := map[advisory.RuntimeQuery][]advisory.Advisory{}
	for len(misses) > 0 {
		batch := misses[:min(32, len(misses))]
		misses = misses[len(batch):]
		results, err := osv.Query(ctx, batch)
		switch {
		case errors.Is(err, advisory.ErrOSVTruncated):
			note = osvTruncatedNote
		case err != nil:
			return advisories, osvUnreachableNote
		}
		for _, q := range batch {
			advisories = append(advisories, results[q]...)
			fresh[q] = results[q]
		}
	}
	if note != "" {
		return advisories, note
	}
	for q, found := range fresh {
		if found == nil {
			found = []advisory.Advisory{}
		}
		_ = s.Store.SaveOSV(ctx, q.Runtime, q.Version, store.JSON(found))
	}
	return advisories, ""
}

func (s *Server) candidateFor(ctx context.Context, a store.Application) (store.SecurityCandidate, error) {
	d, err := s.Store.LatestSucceededDeployment(ctx, a.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.SecurityCandidate{}, errRevisionNotReady
	}
	if err != nil {
		return store.SecurityCandidate{}, err
	}
	return store.SecurityCandidate{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Name: a.Name, Revision: a.Revision, DeploymentID: d.ID}, nil
}

var errRevisionNotReady = errors.New("the current revision has not finished deploying")

// evaluateSecurity rebuilds the inventory, matches both sources, saves the row
// and queues notifications for new findings. A failure is stamped on the row
// with backoff so the app rotates to the back of the queue.
func (s *Server) evaluateSecurity(ctx context.Context, c store.SecurityCandidate, in securityInputs) (store.ApplicationSecurity, error) {
	row, err := s.evaluateSecurityOnce(ctx, c, in)
	if err != nil {
		stamp, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = s.Store.SecurityEvaluationFailed(stamp, c.ApplicationID, "Evaluation failed: "+evaluationCause(err)+".")
	}
	return row, err
}

func evaluationCause(err error) string {
	switch {
	case errors.Is(err, errRevisionNotReady):
		return "the deployment is not ready"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return "timed out"
	}
	return "platform state unavailable"
}

func (s *Server) evaluateSecurityOnce(ctx context.Context, c store.SecurityCandidate, in securityInputs) (store.ApplicationSecurity, error) {
	d, err := s.Store.Deployment(ctx, c.DeploymentID)
	if err != nil {
		return store.ApplicationSecurity{}, err
	}
	if d.ResolvedSpec == nil {
		return store.ApplicationSecurity{}, errRevisionNotReady
	}
	builds, err := s.applicationBuilds(ctx, c.ApplicationID)
	if err != nil {
		return store.ApplicationSecurity{}, err
	}
	inv := buildInventory(c, d.Spec, *d.ResolvedSpec, d.Provenance, builds, s.runtimeEnv(ctx, c.Project, c.Environment))
	var feedFindings, osvFindings []advisory.Finding
	row := store.ApplicationSecurity{ApplicationID: c.ApplicationID, DeploymentID: c.DeploymentID, Revision: c.Revision, Inventory: &inv}
	if in.feed != nil {
		feedFindings = advisory.Match(in.feed.Advisories, inv)
		row.FeedRevision = in.state.Revision
	}
	if in.settings.OSVEnabled {
		osvAdvisories, note := s.osvFindings(ctx, inv)
		osvFindings = advisory.Match(osvAdvisories, inv)
		at := time.Now().UTC()
		if note != "" {
			// Retried in about an hour by the 24 hour candidate window.
			inv.Incomplete = append(inv.Incomplete, note)
			at = at.Add(-23 * time.Hour)
		}
		row.OSVCheckedAt = &at
	}
	if policy, err := s.Store.ApplicationSecurity(ctx, c.ApplicationID); err == nil && policy.AutoPatch {
		if note := s.approvalNote(ctx, policy, time.Now()); note != "" {
			inv.Incomplete = append(inv.Incomplete, note)
		}
	}
	findings := combineFindings(feedFindings, osvFindings)
	now := time.Now().UTC()
	row.Findings, row.CheckedAt, row.EvaluatedAt = findings, &inv.CheckedAt, &now
	if err = s.Store.SaveApplicationSecurity(ctx, row); err != nil {
		return row, err
	}
	s.notifySecurity(ctx, c, findings)
	return s.Store.ApplicationSecurity(ctx, c.ApplicationID)
}

// combineFindings drops an OSV finding that repeats a Hakopod advisory for
// the same service (shared id or alias).
func combineFindings(feed, osv []advisory.Finding) []advisory.Finding {
	if out := advisory.Merge(feed, osv); out != nil {
		return out
	}
	return []advisory.Finding{}
}

func notifiable(f advisory.Finding) bool {
	return f.Confidence == advisory.Confirmed && (f.Source == advisory.SourceHakopod ||
		f.Source == advisory.SourceOSV && (f.Severity == advisory.Critical || f.Severity == advisory.High))
}

func (s *Server) securityLink(applicationID string) string {
	return strings.TrimSuffix(s.Auth.PublicURL, "/") + "/security?application=" + applicationID
}

// notifySecurity queues at most securityNotifyCap new notifications per evaluation.
// ponytail: fixed cap of 5 per pass; the rest are queued on later passes
// because the store only marks a finding notified once it is queued.
func (s *Server) notifySecurity(ctx context.Context, c store.SecurityCandidate, findings []advisory.Finding) int {
	queued := 0
	for _, f := range findings {
		if queued >= securityNotifyCap {
			break
		}
		if !notifiable(f) {
			continue
		}
		// Already-notified findings return false and do not use up the cap.
		if ok, err := s.Store.QueueSecurityNotification(ctx, c, f, s.securityLink(c.ApplicationID)); err == nil && ok {
			queued++
		}
	}
	return queued
}

// approverPrincipal is the deployment grant minted when the approver turned
// automatic patching on. It carries the approver's current project roles, and
// deployments accepted with it are reauthorized by the worker under that key.
func (s *Server) approverPrincipal(ctx context.Context, row store.ApplicationSecurity) (store.Principal, error) {
	if row.GrantID == "" {
		return store.Principal{}, store.ErrForbidden
	}
	return s.Store.KeyPrincipal(ctx, row.GrantID)
}

func (s *Server) approverCanPatch(ctx context.Context, row store.ApplicationSecurity) bool {
	p, err := s.approverPrincipal(ctx, row)
	return err == nil && p.Allows("deployments:write", row.Project, row.Environment, row.ApplicationName)
}

// approvalNote explains why automatic patching is paused, or "".
func (s *Server) approvalNote(ctx context.Context, row store.ApplicationSecurity, now time.Time) string {
	if row.ApprovalExpiresAt != nil && !row.ApprovalExpiresAt.After(now) {
		return "Automatic patching approval expired on " + row.ApprovalExpiresAt.UTC().Format("2 January 2006") + ". Turn it off and on again to approve."
	}
	if !s.approverCanPatch(ctx, row) {
		return approverPausedNote
	}
	return ""
}

// patchEligible is the automatic patch decision for one finding once the tag
// has been resolved again.
func patchEligible(row store.ApplicationSecurity, f advisory.Finding, approver store.Principal, approverErr error, resolvedImage string, feedIsStale bool) bool {
	return !feedIsStale && row.AutoPatch && row.ApprovedBy != "" && f.AutoEligible && approverErr == nil && approver.ID == row.ApprovedBy &&
		approver.Allows("deployments:write", row.Project, row.Environment, row.ApplicationName) &&
		slices.Contains(f.FixedDigests, digestOf(resolvedImage))
}

func patchAttemptID(applicationID, advisoryID string, revision int64, service, deploymentID string) string {
	sum := sha256.Sum256([]byte(applicationID + "|" + advisoryID + "|" + strconv.FormatInt(revision, 10) + "|" + service + "|" + deploymentID))
	return hex.EncodeToString(sum[:])
}

func (s *Server) autoPatch(ctx context.Context, c store.SecurityCandidate, row store.ApplicationSecurity, in securityInputs) bool {
	if !row.AutoPatch || s.Cluster == nil || s.approvalNote(ctx, row, time.Now()) != "" {
		return false // paused; evaluateSecurity records why on the row
	}
	stale := feedStale(in.state, time.Now())
	for _, f := range row.Findings {
		if !f.AutoEligible {
			continue
		}
		approver, approverErr := s.approverPrincipal(ctx, row)
		d, err := s.Store.Deployment(ctx, c.DeploymentID)
		if err != nil || d.ResolvedSpec == nil || d.Revision != c.Revision {
			return false // never patch on top of a newer failed or running revision
		}
		fresh, err := s.Cluster.ResolveScoped(ctx, d.Spec, c.Project, c.Environment)
		if err != nil || !patchEligible(row, f, approver, approverErr, fresh.Services[f.Service].Image, stale) {
			continue
		}
		attempt := store.PatchAttempt{ID: patchAttemptID(c.ApplicationID, f.AdvisoryID, f.AdvisoryRevision, f.Service, c.DeploymentID),
			ApplicationID: c.ApplicationID, AdvisoryID: f.AdvisoryID, AdvisoryRevision: f.AdvisoryRevision, Service: f.Service, FromDeploymentID: c.DeploymentID}
		if ok, err := s.Store.ClaimPatchAttempt(ctx, attempt); err != nil || !ok {
			continue
		}
		// Seed the previous resolved revision with only this service moved to
		// the fixed digest, so no other service changes under the patch.
		seed := *d.ResolvedSpec
		seed.Services = maps.Clone(seed.Services)
		svc := seed.Services[f.Service]
		svc.Image = fresh.Services[f.Service].Image
		seed.Services[f.Service] = svc
		next, err := s.Store.Accept(ctx, approver, c.Project, c.Environment, d.Spec, c.Revision, "security-"+attempt.ID[:40], seed)
		if err != nil {
			s.failPatch(ctx, c, attempt, "The update could not be accepted.")
			return true
		}
		_ = s.Store.UpdatePatchAttempt(ctx, attempt.ID, "accepted", next.ID, "")
		_ = s.Store.RuntimeAudit(ctx, approver, "security.patch.automatic", c.ApplicationID, map[string]any{
			"advisory_id": f.AdvisoryID, "advisory_revision": f.AdvisoryRevision, "service": f.Service, "deployment_id": next.ID, "attempt_id": attempt.ID})
		return true
	}
	return false
}

func hasFinding(row store.ApplicationSecurity, advisoryID, service string) bool {
	return slices.ContainsFunc(row.Findings, func(f advisory.Finding) bool { return f.AdvisoryID == advisoryID && f.Service == service })
}

// reconcilePatchAttempts settles accepted automatic patches.
func (s *Server) reconcilePatchAttempts(ctx context.Context, in securityInputs) {
	attempts, err := s.Store.OpenPatchAttempts(ctx, 50)
	if err != nil {
		return
	}
	for _, a := range attempts {
		if a.DeploymentID == "" {
			if time.Since(a.CreatedAt) > 10*time.Minute {
				if app, err := s.Store.Application(ctx, a.ApplicationID); err == nil {
					s.failPatch(ctx, store.SecurityCandidate{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Name: app.Name, Revision: app.Revision, DeploymentID: a.FromDeploymentID}, a, "The update was not accepted.")
				}
			}
			continue
		}
		d, err := s.Store.Deployment(ctx, a.DeploymentID)
		if err != nil {
			continue
		}
		app, err := s.Store.Application(ctx, a.ApplicationID)
		if err != nil {
			continue
		}
		c := store.SecurityCandidate{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Name: app.Name, Revision: d.Revision, DeploymentID: d.ID}
		switch {
		case d.Status == "failed" || d.Status == "cancelled":
			s.failPatch(ctx, c, a, "The update deployment did not succeed.")
		case app.Revision != d.Revision:
			_ = s.Store.UpdatePatchAttempt(ctx, a.ID, "superseded", "", "")
		case d.Status == "succeeded":
			row, err := s.evaluateSecurity(ctx, c, in)
			if err != nil {
				continue
			}
			if hasFinding(row, a.AdvisoryID, a.Service) {
				s.failPatch(ctx, c, a, "The advisory still applies after the update.")
			} else {
				_ = s.Store.UpdatePatchAttempt(ctx, a.ID, "verified", "", "")
			}
		}
	}
}

func (s *Server) failPatch(ctx context.Context, c store.SecurityCandidate, a store.PatchAttempt, reason string) {
	if s.Store.UpdatePatchAttempt(ctx, a.ID, "failed", "", reason) != nil {
		return
	}
	a.Status, a.Error = "failed", reason
	_, _ = s.Store.QueuePatchResultNotification(ctx, c, a, s.securityLink(c.ApplicationID))
}
