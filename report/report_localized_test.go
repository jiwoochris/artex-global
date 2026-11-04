package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/yukiteruamano/artex/db"
)

// containsHan returns true when the string contains CJK Han (Chinese) characters.
// Hangul (unicode.Hangul), Latin letters, digits and symbols are not Han, so they do not match.
func containsHan(s string) (rune, bool) {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return r, true
		}
	}
	return 0, false
}

func sampleFindingNode(t *testing.T) *db.Node {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"vulnclass": "SQL Injection",
		"name":      "SQL injection in the login form",
		"severity":  "high",
		"summary":   "Error-based SQL injection was confirmed in the login parameter.",
		"evidence":  map[string]string{"poc": "' OR '1'='1"},
	})
	if err != nil {
		t.Fatalf("payload serialization failed: %v", err)
	}
	return &db.Node{ID: 1, Kind: "finding", Payload: payload}
}

func sampleDBFinding() *db.DBFinding {
	return &db.DBFinding{
		ID:              123,
		VulnClass:       "SQL Injection",
		Name:            "SQL injection in the login form",
		Severity:        "high",
		Summary:         "Error-based SQL injection was confirmed in the login parameter.",
		Evidence:        "' OR '1'='1",
		Status:          "confirmed",
		Report:          "Detailed analysis body.",
		CreatedAt:       time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC),
		EvidenceVersion: 2,
		TaskDescription: "Penetration test of the demo target",
		TrafficBindings: []db.FindingTrafficBinding{
			{
				ID:   9,
				Role: "request",
				Note: "Injection payload delivery point",
				Snapshot: db.TrafficEvidenceSnapshot{
					Method: "POST",
					URL:    "https://sandbox.local/login",
					Status: 200,
				},
			},
		},
	}
}

// TestReportMarkdownLocalized verifies that the skeleton of the exploration graph report (report.Markdown) is English and
// that it contains no Chinese Han characters at all.
func TestReportMarkdownLocalized(t *testing.T) {
	out := Markdown(Input{
		Title:       "Demo task",
		Goal:        "Full sandbox sweep",
		GeneratedAt: time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC),
		AssetCounts: map[string]int{"host": 2, "url": 5},
		Findings:    []*db.Node{sampleFindingNode(t)},
	})
	if r, ok := containsHan(out); ok {
		t.Fatalf("the report skeleton still contains the Chinese Han character %q:\n%s", string(r), out)
	}
	for _, want := range []string{
		"# Penetration Test Report: Demo task",
		"- **Task goal**: Full sandbox sweep",
		"- **Generated at**:",
		"## Summary",
		"- Confirmed findings: **1**",
		"## Findings",
		"**PoC / Evidence:**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report skeleton is missing %q", want)
		}
	}
}

// TestReportMarkdownEmptyLocalized verifies the notice text shown when there are no findings.
func TestReportMarkdownEmptyLocalized(t *testing.T) {
	out := Markdown(Input{Title: "empty task", GeneratedAt: time.Now()})
	if r, ok := containsHan(out); ok {
		t.Fatalf("the empty report still contains the Chinese ideograph %q:\n%s", string(r), out)
	}
	if !strings.Contains(out, "_No findings were confirmed in this run._") {
		t.Errorf("the empty report notice is missing:\n%s", out)
	}
}

// TestFindingsMarkdownLocalized verifies that the vulnerability summary/detail Markdown export skeleton and the severity labels are
// English and contain no Chinese ideographs.
func TestFindingsMarkdownLocalized(t *testing.T) {
	f := sampleDBFinding()
	out := FindingsMarkdown([]*db.DBFinding{f}, time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC))
	if r, ok := containsHan(out); ok {
		t.Fatalf("the vulnerability summary report still contains the Chinese ideograph %q:\n%s", string(r), out)
	}
	for _, want := range []string{
		"# Vulnerability Summary Report",
		"- **Generated at**:",
		"- **Total findings**: 1",
		"## Summary",
		"| Severity | Count |",
		"| Critical |", "| High |", "| Medium |", "| Low |", // severity labels (same as UI status.severity)
		"## Finding details",
		"- **Type**: SQL Injection",
		"- **Status**: confirmed",
		"- **Task**: Penetration test of the demo target",
		"- **Found at**:",
		"**Evidence:**",
		"**Detailed report:**",
		"## Related traffic evidence",
		"Evidence version: 2, binding count: 1.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary report is missing %q", want)
		}
	}

	single := SingleFindingMarkdown(f, time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC))
	if r, ok := containsHan(single); ok {
		t.Fatalf("the single-finding report still contains the Chinese ideograph %q:\n%s", string(r), single)
	}
	for _, want := range []string{
		"- **Severity**: high",
		"## Summary",
		"## Evidence",
		"## Detailed report",
		"[Request message](evidence/123/9/request.http)",
		"[Response message](evidence/123/9/response.http)",
	} {
		if !strings.Contains(single, want) {
			t.Errorf("the single-finding report is missing %q", want)
		}
	}
}

// TestFindingsCSVLocalized verifies that the CSV export header is English and contains no Chinese ideographs.
// (Only the header row after the BOM is generated text. Data values are left as they are.)
func TestFindingsCSVLocalized(t *testing.T) {
	raw := FindingsCSV([]*db.DBFinding{sampleDBFinding()})
	out := strings.TrimPrefix(string(raw), "\xEF\xBB\xBF")
	header := strings.SplitN(out, "\n", 2)[0]
	if r, ok := containsHan(header); ok {
		t.Fatalf("the CSV header still contains the Chinese ideograph %q: %s", string(r), header)
	}
	for _, col := range []string{"ID", "Name", "Type", "Severity", "Status", "Task", "Found at", "Summary", "Traffic evidence count", "Traffic evidence IDs"} {
		if !strings.Contains(header, col) {
			t.Errorf("the CSV header is missing the %q column: %s", col, header)
		}
	}
}
