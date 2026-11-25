package server

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression guard that keeps the archive package delete-staging error message in task_archive_package.go
// in Korean. The Korean check reuses assertKoreanError, built by F3a (Hangul present, zero Chinese Han characters).
// If anyone reverts this literal to Chinese, this test fails.
//
// Call graph decision (see the comment on the errArchiveStagedAndOriginalCoexist constant): this error is stored
// in the task_archives.error column via FailTaskArchiveJob by the background archive worker runOneTaskArchiveJob
// (task_archives.go:102), and TaskArchivesPanel in tasks/page.tsx shows it directly on screen as {archive.error};
// it is user-facing only (zero actool/planner feedback). Sibling errors on the same propagation path
// (archiveTask, validateArchivePath) are already Korean, so this translation is consistent with them.
//
// stageTaskArchivePackageDelete is a pure file function that never touches the DB, so on this DB-less host we can
// create both the original file and the delete staging file (.deleting-N) and drive the actual contradiction branch.
func TestTaskArchiveStagedAndOriginalCoexistErrorLocalized(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := taskArchivePath(dataDir, 21, "42")
	if err := os.MkdirAll(filepath.Dir(archivePath), archiveDirMode); err != nil {
		t.Fatal(err)
	}
	// Create the contradictory state where both the original archive package and the delete staging file exist.
	if err := os.WriteFile(archivePath, []byte("original"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	staged := archivePath + ".deleting-21"
	if err := os.WriteFile(staged, []byte("archive"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	_, moved, err := stageTaskArchivePackageDelete(archivePath, 21)
	if err == nil {
		t.Fatalf("원본과 삭제 임시 파일이 공존하면 오류가 반환되어야 합니다 (moved=%v)", moved)
	}
	if moved {
		t.Fatal("공존 모순 상태에서는 moved 가 true 가 되어서는 안 됩니다")
	}
	assertKoreanError(t, "staged_and_original_coexist", err.Error())
}
