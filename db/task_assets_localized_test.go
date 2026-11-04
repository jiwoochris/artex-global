package db

import "testing"

// TestManualTaskScopeSummaryLocalized pins the manual task-scope provenance label to its exact
// English text. It is stored on task_asset_links.source_summary (AddTaskScope / SetTaskAssetSource)
// and rendered verbatim on the task detail sessions/assets tabs, so a stale translation would
// surface mixed-language provenance labels in the same panel. The existing task_assets_test.go
// already asserts the stored value equals this constant via the symbol, so pinning the constant
// here protects the user-facing text too.
func TestManualTaskScopeSummaryLocalized(t *testing.T) {
	want := "Added manually by the user on the test assets page"
	if manualTaskScopeSummary != want {
		t.Fatalf("manualTaskScopeSummary: want %q, got %q", want, manualTaskScopeSummary)
	}
}
