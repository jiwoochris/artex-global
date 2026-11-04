package selfupdate

// Regression test (F20) that pins the user-facing error strings of the self-update path.
// update.go's updateHub exposes the errors returned by Stage/FetchLatest/Rollback as
// progress.error, writeErr body and boot_notice, straight to the frontend (update-card), so
// these exact English messages are what the user ends up reading. Logs (log.Printf) and
// comments are out of scope.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// wantError asserts that err carries exactly the English message the UI shows.
func wantError(t *testing.T, label string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error but got nil", label)
	}
	if got := err.Error(); got != want {
		t.Errorf("%s: want %q, got %q", label, want, got)
	}
}

// rtFunc injects a fake response into http.Client. Nothing actually connects to GitHub.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubClient(status int, body string) *http.Client {
	return &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

// TestCheckURLErrorsLocalized: the existing TestCheckURLRejectsNonGitHub only looks at whether the URL was
// rejected and not at the wording. Here we pin the exact message of both rejection paths.
func TestCheckURLErrorsLocalized(t *testing.T) {
	nonHTTPS := checkURL(mustParse(t, "http://api.github.com/x"))
	wantError(t, "non-https", nonHTTPS, "refusing a non-HTTPS address: http://api.github.com")

	nonGitHub := checkURL(mustParse(t, "https://evil.example.com/x"))
	wantError(t, "non-github-host", nonGitHub, "refusing an address that is not a GitHub domain: evil.example.com")
}

// TestFetchLatestErrorsLocalized pins the five errors of the version-check path (rate limited,
// not published, other status, parse failure, missing tag). The fake transport means no network is used.
func TestFetchLatestErrorsLocalized(t *testing.T) {
	ctx := context.Background()

	_, errRate := FetchLatest(ctx, stubClient(http.StatusForbidden, ""))
	wantError(t, "rate-limited", errRate, "hit the GitHub API rate limit (60 requests per hour); try again later")

	_, errNF := FetchLatest(ctx, stubClient(http.StatusNotFound, ""))
	wantError(t, "not-found", errNF, "repository Autumn-27/artex has no stable release yet")

	_, errStatus := FetchLatest(ctx, stubClient(http.StatusInternalServerError, ""))
	wantError(t, "bad-status", errStatus, "GitHub returned status code 500")

	// The wrapped stdlib parse error is not ours to pin, so only our English prefix is asserted.
	_, errJSON := FetchLatest(ctx, stubClient(http.StatusOK, "{not json"))
	if errJSON == nil {
		t.Fatal("bad-json: expected an error but got nil")
	}
	if got := errJSON.Error(); !strings.HasPrefix(got, "could not parse the release: ") {
		t.Errorf("bad-json: want the prefix %q, got %q", "could not parse the release: ", got)
	}

	_, errTag := FetchLatest(ctx, stubClient(http.StatusOK, "{}"))
	wantError(t, "missing-tag", errTag, "the release has no tag")
}

// TestStageMissingAssetLocalized: when the release has no package for the current platform, Stage
// stops with this exact error before downloading (FindAsset fails → no network access). The
// writability probe that runs first passes for the test binary's own directory.
func TestStageMissingAssetLocalized(t *testing.T) {
	rel := &Release{TagName: "v9.9.9"} // Assets is empty
	err := Stage(context.Background(), &http.Client{}, rel, "0.0.1", nil)
	name := AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	want := fmt.Sprintf("This version does not provide a release package for %s/%s (%s is missing)", runtime.GOOS, runtime.GOARCH, name)
	wantError(t, "stage-missing-asset", err, want)
}
