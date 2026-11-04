package traffic

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSpillWritesPrivateBlobs pins L-5 for the traffic recorder: captured bodies
// above the inline limit land in the blob store as 0600 files inside 0700
// directories (they were 0644/0755 before), so a shared host cannot read captured
// third-party credentials.
func TestSpillWritesPrivateBlobs(t *testing.T) {
	dir := t.TempDir()
	tr := &Traffic{dir: dir}

	sb := tr.spill(bytes.Repeat([]byte("a"), maxInlineBody+1), "text/plain")
	if sb.hash == "" {
		t.Fatal("a body above maxInlineBody must spill to the blob store")
	}
	blobDir := filepath.Join(dir, "_blobs", "sha256", sb.hash[:2])
	blobPath := filepath.Join(blobDir, sb.hash+".bin")

	fi, err := os.Stat(blobPath)
	if err != nil {
		t.Fatalf("stat blob: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("blob mode = %04o, want 0600", got)
	}

	di, err := os.Stat(blobDir)
	if err != nil {
		t.Fatalf("stat blob dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("blob dir mode = %04o, want 0700", got)
	}
}
