package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSyncScopeSentryConstantsLocalized guards F3b (sync_scopesentry.go): every
// user-facing message constant — surfaced either as a writeErr HTTP body or inside
// the sync result's warnings/errors JSON arrays — is its exact English source text.
// Reverting any literal to another language, or rewording it, fails this test.
func TestSyncScopeSentryConstantsLocalized(t *testing.T) {
	cases := []struct {
		label       string
		msg         string
		wantEnglish string
	}{
		{"errSSDataSourceMissingFmt", errSSDataSourceMissingFmt, "Data source %s does not exist. Please create it first"},
		{"errSSDataSourceNoURLFmt", errSSDataSourceNoURLFmt, "No URL is set for data source %s. Please set it first"},
		{"errSSListProjectsPrefix", errSSListProjectsPrefix, "Failed to call list_projects_data: "},
		{"errSSParseProjectsPrefix", errSSParseProjectsPrefix, "Failed to parse the project list: "},
		{"errSSListTasksPrefix", errSSListTasksPrefix, "Failed to call list_tasks: "},
		{"errSSParseTasksPrefix", errSSParseTasksPrefix, "Failed to parse the task list: "},
		{"errSSDimension", errSSDimension, "The dimension value must be either project or task"},
		{"errSSTargetsEmpty", errSSTargetsEmpty, "targets cannot be empty"},
		{"warnSSProjectMetaFmt", warnSSProjectMetaFmt, "Could not fetch details for project %s: %v"},
		{"warnSSCompanyCreateFmt", warnSSCompanyCreateFmt, "Failed to create company %s: %v"},
		{"warnSSUnknownAssetPrefix", warnSSUnknownAssetPrefix, "Skipped unknown asset type: "},
		{"errSSFetchFmt", errSSFetchFmt, "Failed to fetch asset %s(%s): %v"},
		{"warnSSTruncatedFmt", warnSSTruncatedFmt, "%s(%s) was truncated at the %d-row limit"},
		{"errSSParseSubdomainPrefix", errSSParseSubdomainPrefix, "Failed to parse the subdomain response: "},
		{"errSSParseAppPrefix", errSSParseAppPrefix, "Failed to parse the app response: "},
		{"errSSParseServicePrefix", errSSParseServicePrefix, "Failed to parse the service response: "},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	// dimension/targets validation keep their JSON field names and enum values.
	if !strings.Contains(errSSDimension, "project") || !strings.Contains(errSSDimension, "task") {
		t.Fatalf("errSSDimension lost its enum values: %q", errSSDimension)
	}
}

// TestSyncScopeSentryFormattedLocalized exercises the exact Sprintf/Errorf format
// strings used by the sync handler so a verb mismatch or a reverted literal is
// caught by running the real format operation (not just inspecting the constant).
func TestSyncScopeSentryFormattedLocalized(t *testing.T) {
	ds := fmt.Errorf(errSSDataSourceMissingFmt, scopeSentryMCPName).Error()
	assertEnglishMessage(t, "scopeSentryClient/missing", ds,
		fmt.Sprintf("Data source %s does not exist. Please create it first", scopeSentryMCPName))
	if !strings.Contains(ds, "ScopeSentry") {
		t.Fatalf("the data source name disappeared: %q", ds)
	}

	noURL := fmt.Errorf(errSSDataSourceNoURLFmt, scopeSentryMCPName).Error()
	assertEnglishMessage(t, "scopeSentryClient/noURL", noURL,
		fmt.Sprintf("No URL is set for data source %s. Please set it first", scopeSentryMCPName))
	if !strings.Contains(noURL, "URL") {
		t.Fatalf("the URL token disappeared: %q", noURL)
	}

	meta := fmt.Sprintf(warnSSProjectMetaFmt, "proj-1", errors.New("boom"))
	assertEnglishMessage(t, "warnSSProjectMetaFmt", meta, "Could not fetch details for project proj-1: boom")
	if !strings.Contains(meta, "proj-1") || !strings.Contains(meta, "boom") {
		t.Fatalf("the project/source error was not substituted: %q", meta)
	}

	company := fmt.Sprintf(warnSSCompanyCreateFmt, "ACME", errors.New("boom"))
	assertEnglishMessage(t, "warnSSCompanyCreateFmt", company, "Failed to create company ACME: boom")
	if !strings.Contains(company, "ACME") {
		t.Fatalf("the company name was not substituted: %q", company)
	}

	fetch := fmt.Sprintf(errSSFetchFmt, "subdomain", "t1", errors.New("boom"))
	assertEnglishMessage(t, "errSSFetchFmt", fetch, "Failed to fetch asset subdomain(t1): boom")
	if !strings.Contains(fetch, "subdomain") || !strings.Contains(fetch, "t1") {
		t.Fatalf("the asset type/target was not substituted: %q", fetch)
	}

	truncated := fmt.Sprintf(warnSSTruncatedFmt, "app", "t1", syncMaxPerType)
	assertEnglishMessage(t, "warnSSTruncatedFmt", truncated, "app(t1) was truncated at the 5000-row limit")
	if !strings.Contains(truncated, "5000") {
		t.Fatalf("the truncated count was not substituted: %q", truncated)
	}
}

// TestSyncScopeSentryIngestParseErrorsLocalized runs ssIngest on malformed JSON for
// each asset kind. The unmarshal failure returns before any store access, so this is
// a real code path (nil AssetStore never dereferenced) that pins the parse-error
// strings to the English source text.
func TestSyncScopeSentryIngestParseErrorsLocalized(t *testing.T) {
	s := &Server{}
	bad := json.RawMessage("not json")
	for _, kind := range []string{"subdomain", "app", "service"} {
		got := s.ssIngest(nil, kind, bad, map[string]int{})
		if got == "" {
			t.Fatalf("%s: the error string is empty for broken JSON", kind)
		}
		assertEnglishPrefix(t, "ssIngest/"+kind, got, "Failed to parse the "+kind+" response: ")
		if !strings.Contains(got, kind) {
			t.Fatalf("%s: the asset type prefix disappeared: %q", kind, got)
		}
	}
}
