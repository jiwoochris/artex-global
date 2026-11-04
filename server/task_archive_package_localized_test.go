package server

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression guard that keeps the archive-package delete-preparation error message in
// task_archive_package.go as its exact English source text (assertEnglishMessage).
// Reverting this literal to another language fails this test.
//
// Call-graph verdict (see the errArchiveStagedAndOriginalCoexist constant comment): this error is
// written into the task_archives.error column by the background archive worker
// runOneTaskArchiveJob(task_archives.go:102) via FailTaskArchiveJob, and TaskArchivesPanel in
// tasks/page.tsx displays it directly as {archive.error}, so it is user-facing only (no actool
// or planner feedback). Sibling errors on the same propagation path (archiveTask, validateArchivePath) are English too, so this is consistent with them.
//
// stageTaskArchivePackageDelete is a pure file function that never touches the DB, so on this
// DB-less host it can create both the original file and the delete temp file (.deleting-N) to drive the real contradiction branch.
func TestTaskArchiveStagedAndOriginalCoexistErrorLocalized(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := taskArchivePath(dataDir, 21, "42")
	if err := os.MkdirAll(filepath.Dir(archivePath), archiveDirMode); err != nil {
		t.Fatal(err)
	}
	// Create the contradictory state where both the original archive package and the delete temp file exist.
	if err := os.WriteFile(archivePath, []byte("original"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	staged := archivePath + ".deleting-21"
	if err := os.WriteFile(staged, []byte("archive"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	_, moved, err := stageTaskArchivePackageDelete(archivePath, 21)
	if err == nil {
		t.Fatalf("an error must be returned when the original and the delete temp file coexist (moved=%v)", moved)
	}
	if moved {
		t.Fatal("moved must not become true in the coexist contradiction state")
	}
	assertEnglishMessage(t, "staged_and_original_coexist", err.Error(),
		"The archive package's original file and its deletion temp file both exist")
}
