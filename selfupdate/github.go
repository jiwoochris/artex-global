package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo is the release source. Hard-coded rather than configurable: a configurable
// update source would be a remote code execution channel for anyone able to edit the
// configuration, which is unacceptable for a penetration testing platform.
const Repo = "Autumn-27/artex"

// latestURL is GitHub's "latest stable release" endpoint. It skips prereleases and
// drafts automatically.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts restricts the domains the upgrade path may reach. Together with
// checkRedirect below, any hop redirected to a host outside the list fails
// immediately - this is the first gate against DNS poisoning / a man in the middle
// swapping the binary; the second is the SHA256SUMS comparison.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // object storage that release assets actually land on
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release holds the GitHub Release fields we care about.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset is one file attached to a Release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient builds an HTTP client that only talks to GitHub domains. An empty proxy
// means a direct connection.
//
// It deliberately does not reuse the default Transport: the upgrade path must force
// TLS and verify certificates, and must not be affected by an InsecureSkipVerify set
// somewhere else.
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // downloading a whole package must not be capped by a request-level timeout
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL forces https plus the domain allowlist.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("refusing a non-HTTPS address: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("refusing an address that is not a GitHub domain: %s", u.Hostname())
	}
	return nil
}

// FetchLatest queries the latest stable release.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub (a global proxy can be configured in system settings): %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// The unauthenticated GitHub API allows 60 requests per hour per IP, which is easy
		// to hit when an egress IP is shared.
		return nil, errors.New("hit the GitHub API rate limit (60 requests per hour); try again later")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("repository %s has no stable release yet", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub returned status code %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("could not parse the release: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, errors.New("the release has no tag")
	}
	return &rel, nil
}

// AssetName returns the release package name for the current platform, matching
// package_binary in build.sh: artex-<version>-<os>-<arch>.zip (the version has no v
// prefix).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset looks up an asset by name in the Release.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
