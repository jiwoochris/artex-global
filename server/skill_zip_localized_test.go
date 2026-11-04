package server

import (
	"fmt"
	"strings"
	"testing"
)

// TestSkillZipErrorsLocalized guards the F3b skill_zip.go bundle: every user-facing
// skill-upload error response (fsUploadSkill surfaces these via
// writeErr(400, err.Error()), see server_mgmt.go) is its exact English source text,
// and the zip-method display names are the English labels shown in that message.
// Reverting any of these literals to another language fails here.
func TestSkillZipErrorsLocalized(t *testing.T) {
	// Message constants rendered with sample arguments.
	assertEnglishMessage(t, "errSkillZipParse",
		fmt.Errorf(errSkillZipParse, fmt.Errorf("boom")).Error(),
		"Failed to parse the archive file (must be zip format): boom")
	assertEnglishMessage(t, "errSkillZipEncrypted",
		fmt.Sprintf(errSkillZipEncrypted, "demo/SKILL.md"),
		"The archive is encrypted (demo/SKILL.md). Please upload an unencrypted zip file.")
	assertEnglishMessage(t, "errSkillZipUnsupported",
		fmt.Sprintf(errSkillZipUnsupported, "LZMA", 14, "demo/SKILL.md"),
		"Unsupported compression method: LZMA (method 14), file demo/SKILL.md. "+
			"Please recompress it with Store or Deflate "+
			"(in 7-Zip/WinRAR pick Deflate, or use the operating system's built-in compressor or the command line zip -r).")

	// A non-zip upload must reach the user as the English hint, not a raw stdlib error.
	if _, err := newSkillZipReader([]byte("this is not a zip archive")); err == nil {
		t.Fatal("a non-zip input must return an error")
	} else {
		assertEnglishPrefix(t, "newSkillZipReader", err.Error(),
			"Failed to parse the archive file (must be zip format): zip:")
		if !strings.Contains(err.Error(), "archive file") {
			t.Fatalf("parse error = %q, want the 'archive file' hint", err.Error())
		}
	}

	// Every zip-method display name is a fixed English label.
	cases := []struct {
		method      uint16
		wantEnglish string
	}{
		{zipMethodStore, "Store"},
		{zipMethodDeflate, "Deflate"},
		{zipMethodDeflate64, "Deflate64"},
		{zipMethodBzip2, "bzip2"},
		{zipMethodLZMA, "LZMA"},
		{zipMethodZstdPKW, "Zstandard"},
		{zipMethodZstd, "Zstandard"},
		{zipMethodXZ, "XZ"},
		{zipMethodJPEG, "JPEG"},
		{zipMethodWavPack, "WavPack"},
		{zipMethodPPMd, "PPMd"},
		{zipMethodAES, "AES encryption"},
		{0xffff, "unknown"},
	}
	for _, c := range cases {
		assertEnglishMessage(t, fmt.Sprintf("zipMethodName(%d)", c.method), zipMethodName(c.method), c.wantEnglish)
	}
	// The table above lists every label in zipMethodNames plus the unknown fallback.
	if len(zipMethodNames) != len(cases)-1 {
		t.Fatalf("zipMethodNames has %d entries but %d labels are pinned", len(zipMethodNames), len(cases)-1)
	}
}
