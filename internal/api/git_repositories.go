package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type gitRepository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// Listing uses a separate metadata-only token. Build and source tokens remain
// narrowed to a single repository in githubInstallationToken.
func (s *Server) listGitRepositories(w http.ResponseWriter, r *http.Request) {
	if !gitManager(w, r) {
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100 {
			problem(w, 400, "invalid_page", "Repository page must be between 1 and 100")
			return
		}
	}
	c, err := s.readGitConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		authFailure(w, err)
		return
	}
	if c.Provider != "github" || c.AuthKind != "github_app" || !c.Enabled || c.InstallationID <= 0 {
		problem(w, 400, "repository_listing_unavailable", "Choose an enabled GitHub App installation to list repositories")
		return
	}
	credentials, err := s.decodeGitCredentials(c)
	if err != nil {
		failure(w, err)
		return
	}
	var token struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err = s.githubAppAPI(r.Context(), credentials, "POST", "/app/installations/"+strconv.FormatInt(c.InstallationID, 10)+"/access_tokens", map[string]any{"permissions": map[string]string{"metadata": "read"}}, &token)
	if err != nil || token.Token == "" || len(token.Token) > 16384 || !token.ExpiresAt.After(time.Now().Add(10*time.Second)) {
		problem(w, 502, "repository_listing_unavailable", "Could not read this installation. Check its GitHub permissions or enter the repository manually")
		return
	}
	base := s.githubAPIURL
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", base+"/installation/repositories?per_page=25&page="+strconv.Itoa(page), nil)
	if err != nil {
		failure(w, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{Timeout: 15 * time.Second}
	if s.githubHTTP != nil {
		*client = *s.githubHTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		problem(w, 502, "repository_listing_unavailable", "GitHub is unavailable. Retry or enter the repository manually")
		return
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	var result struct {
		Repositories []gitRepository `json:"repositories"`
	}
	if res.StatusCode != 200 || err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, &result) != nil || len(result.Repositories) > 25 {
		problem(w, 502, "repository_listing_unavailable", "GitHub could not list repositories. Check the installation access and retry")
		return
	}
	items := make([]gitRepository, 0, len(result.Repositories))
	for _, repo := range result.Repositories {
		if !repositoryPattern.MatchString(repo.FullName) || !strings.EqualFold(strings.SplitN(repo.FullName, "/", 2)[0], c.Account) || len(repo.DefaultBranch) > 200 {
			failure(w, errors.New("invalid GitHub repository metadata"))
			return
		}
		items = append(items, repo)
	}
	next := 0
	if len(items) == 25 && page < 100 {
		next = page + 1
	}
	write(w, 200, map[string]any{"items": items, "next_page": next})
}

func normalizeSourceRepository(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".git")
}
