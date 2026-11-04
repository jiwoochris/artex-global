package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// maxChatUpload caps a single chat-attachment upload request (memory + spill).
const maxChatUpload = 128 << 20 // 128 MiB

// chatupload's user-facing error responses. writeErr exposes them verbatim to the UI.
// Terminology: attachment; scope/id/file are request field names, so the original
// spelling is kept. scope validation follows the "must be one of …" pattern from
// intercept.go, the task-deleting wording matches goals_api.go, and "invalid id"
// mirrors workspace.go errWsIllegalPath ("Invalid path"). The three "failed"
// strings are prefix constants concatenated with err.Error(), following the
// task_archives.go:229 precedent.
const (
	errChatUploadScopeInvalid = "The scope value must be one of task, session, or staging"
	errChatUploadBadID        = "Invalid id"
	errChatUploadTaskDeleting = "The task is being deleted, so attachments cannot be uploaded"
	errChatUploadMkdir        = "Failed to create directory: "
	errChatUploadParse        = "Failed to parse the upload, or it exceeded the size limit: "
	errChatUploadNoFile       = "There is no file to upload (form field file)"
	errChatUploadSaveFailed   = "Failed to save: "
)

// safeChatID guards the {id} path segment against traversal — task ids are numeric,
// session ids are alnum/_/- ; anything with "/" or ".." is rejected.
var safeChatID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// chatAttachment is one uploaded file as the frontend + agent see it. Path is relative
// to the chat's working dir (e.g. "uploads/report.txt"), which is the agent's CWD, so
// it can Read/Bash the file directly; Name/Size drive the UI card.
type chatAttachment struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Abs is the absolute on-disk path (m.dir is already absolute). While staging before
	// task creation (scope=staging) the frontend uses it to write the prompt into the
	// description; task/session paths are composed by composeAgentMessage on the backend
	// and don't depend on this field.
	Abs string `json:"abs,omitempty"`
}

// chatUpload implements method-1 file support: it saves one or more files into a chat's
// working dir under uploads/, so the agent opens them with its existing Read/Bash tools
// and the sent message carries their paths. No LLM-layer change, no multimodal.
//
// POST /api/chat/upload?scope=task|session|staging&id=<id>, multipart field "file"
// (repeatable). Returns {attachments:[{name,path,size,abs}]}. Target dir mirrors the
// agent CWD layout:
//
//	scope=task    → <workDir>/tasks/<id>/uploads/
//	scope=session → <workDir>/sessions/<id>/uploads/
//	scope=staging → <workDir>/drafts/<id>/uploads/   (staging before task creation: the task
//	                has no ID yet, so files land here and the frontend writes their
//	                absolute paths into the task description)
func (s *Server) chatUpload(w http.ResponseWriter, r *http.Request) {
	var sub string
	taskScoped := false
	switch r.URL.Query().Get("scope") {
	case "task":
		sub = "tasks"
		taskScoped = true
	case "session":
		sub = "sessions"
	case "staging":
		sub = "drafts"
	default:
		writeErr(w, 400, errChatUploadScopeInvalid)
		return
	}
	id := r.URL.Query().Get("id")
	if !safeChatID.MatchString(id) {
		writeErr(w, 400, errChatUploadBadID)
		return
	}
	if taskScoped {
		if s.m.ResolveTask(id) == nil {
			writeErr(w, 404, "task not found")
			return
		}
		if !s.engine.beginTaskOperation(id) {
			writeErr(w, http.StatusConflict, errChatUploadTaskDeleting)
			return
		}
		defer s.engine.decInflight(id)
	}
	dir := filepath.Join(s.m.dir, sub, id, "uploads")
	// 0700: attachments can carry customer data, and the tree is only browsed by the
	// server and the agent tools it runs as the same user.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, errChatUploadMkdir+err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxChatUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, errChatUploadParse+err.Error())
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, 400, errChatUploadNoFile)
		return
	}
	out := make([]chatAttachment, 0, len(files))
	for _, hdr := range files {
		name := filepath.Base(hdr.Filename) // strip any path component
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			continue
		}
		dest := uniqueUploadPath(dir, name)
		if err := saveUpload(hdr, dest); err != nil {
			writeErr(w, 500, errChatUploadSaveFailed+err.Error())
			return
		}
		base := filepath.Base(dest)
		out = append(out, chatAttachment{Name: base, Path: "uploads/" + base, Size: hdr.Size, Abs: dest})
	}
	writeJSON(w, 200, map[string]any{"attachments": out})
}

// uniqueUploadPath returns dir/name, or dir/name-1, dir/name-2… when it already exists,
// so re-uploading the same filename never clobbers a prior attachment.
func uniqueUploadPath(dir, name string) string {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// composeAgentMessage appends an attachment manifest to the user's message so the agent
// knows which files were uploaded and where to Read them. baseDir is the agent's working
// dir (its CWD); we emit ABSOLUTE paths (baseDir + relative) so the agent can Read/Bash
// them unambiguously regardless of how it interprets relative paths.
func composeAgentMessage(msg string, atts []chatAttachment, baseDir string) string {
	if len(atts) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(msg)
	// [F10 boundary call · model input preserved] The attachment manifest header
	// (`[Attachments uploaded by the user] …Read/Bash…`) is prompt text, not display
	// text. composeAgentMessage's return value is sent to the agent via
	// runConversation/ma.Chat, so this literal instructs the model to open the uploaded
	// files with Read/Bash (BRIEF boundary #1). Transcripts render attachments from the
	// JSON via userActivityWithAttachments, so the header itself never reaches a display
	// surface (same shape as F16: model-input only).
	b.WriteString("\n\n[Attachments uploaded by the user] (absolute paths; use Read/Bash to view them when needed):")
	for _, a := range atts {
		fmt.Fprintf(&b, "\n- %s (%s)", filepath.Join(baseDir, a.Path), humanBytes(a.Size))
	}
	return b.String()
}

// userActivityWithAttachments builds the persisted 'user' activity. With attachments,
// Detail holds JSON {text, attachments} so the transcript renders text + attachment
// cards; Summary stays the plain text (the list payload omits Detail, lazy-loaded).
func userActivityWithAttachments(worker, text string, atts []chatAttachment) db.Activity {
	a := db.Activity{Worker: worker, Kind: "user", Summary: text}
	if len(atts) > 0 {
		blob, _ := json.Marshal(map[string]any{"text": text, "attachments": atts})
		a.Detail = string(blob)
	}
	return a
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
