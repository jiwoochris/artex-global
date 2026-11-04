// Linux-only platform policy (SPEC.md REQ-PLAT-1).
//
// ARTEX ships a Go binary plus a Debian Trixie container image; Docker on Windows and
// macOS is already a Linux VM, so the product has no native Windows/macOS runtime path.
// The Windows supervisor, the darwin/windows build targets and their documentation were
// removed on purpose. This test keeps them from creeping back: it is the executable form
// of the decision, so a future contributor learns about it from a failing test rather than
// from a reviewer's memory.
//
// Note what this test deliberately does NOT touch: detections/, docs/defense.md and the
// agent prompts keep every piece of knowledge about attacking and defending Windows and
// macOS *targets*. That material is about the machines being tested, not about where ARTEX
// runs.
package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenScriptExts are the extensions a Windows-native supervisor or automation script
// would use. Linux shell scripts (.sh) are the only supported form.
var forbiddenScriptExts = []string{".bat", ".cmd", ".ps1", ".vbs"}

// walkSkips are the directories that hold generated or vendored content, where a stray
// Windows file is not a statement about support.
var walkSkips = map[string]bool{
	".git": true, "node_modules": true, ".next": true, "out": true,
	"dist": true, "data": true, ".pnpm-store": true,
}

func TestNoWindowsScripts(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are not this test's business
		}
		if entry.IsDir() {
			if walkSkips[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		for _, forbidden := range forbiddenScriptExts {
			if ext == forbidden {
				offenders = append(offenders, strings.TrimPrefix(path, root+string(os.PathSeparator)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	for _, offender := range offenders {
		t.Errorf("%s: Windows script found; ARTEX is Linux only (SPEC.md REQ-PLAT-1)", offender)
	}
}

func TestBuildScriptTargetsLinuxOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "build.sh"))
	if err != nil {
		t.Fatalf("read build.sh: %v", err)
	}
	text := string(raw)

	t.Run("release targets default", func(t *testing.T) {
		for _, line := range strings.Split(text, "\n") {
			if !strings.HasPrefix(line, "RELEASE_TARGETS_DEFAULT=") {
				continue
			}
			for _, platform := range []string{"windows", "darwin"} {
				if strings.Contains(line, platform) {
					t.Errorf("RELEASE_TARGETS_DEFAULT still targets %s: %s", platform, line)
				}
			}
			if !strings.Contains(line, "linux/amd64") || !strings.Contains(line, "linux/arm64") {
				t.Errorf("RELEASE_TARGETS_DEFAULT must keep both Linux targets: %s", line)
			}
			return
		}
		t.Error("RELEASE_TARGETS_DEFAULT not found in build.sh")
	})

	t.Run("no windows artifacts in packaging", func(t *testing.T) {
		for _, forbidden := range []string{"artex.exe", "start.bat", "--force-macos"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("build.sh still mentions %q; the Linux-only package ships start.sh and a plain `artex` binary", forbidden)
			}
		}
	})
}

func TestReleaseMatrixIsLinuxOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	var matrixLines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "goos:") {
			matrixLines = append(matrixLines, strings.TrimSpace(line))
		}
	}
	if len(matrixLines) == 0 {
		t.Fatal("no matrix `goos:` entries found in release.yml")
	}
	for _, entry := range matrixLines {
		if !strings.Contains(entry, "goos: linux") {
			t.Errorf("release matrix entry is not Linux: %s", entry)
		}
	}
	// The docker job consumes these artifact names; a renamed matrix leg would break it
	// silently, so the two are asserted together.
	for _, artifact := range []string{"bin-linux-amd64", "bin-linux-arm64"} {
		if !strings.Contains(string(raw), artifact) {
			t.Errorf("release.yml no longer references the %s artifact", artifact)
		}
	}
}

func TestGitAttributesHasNoWindowsRules(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "*.bat") {
		t.Error(".gitattributes still carries a rule for *.bat files")
	}
	if !strings.Contains(text, "*.sh") || !strings.Contains(text, "eol=lf") {
		t.Error(".gitattributes must keep the *.sh eol=lf rule")
	}
}
