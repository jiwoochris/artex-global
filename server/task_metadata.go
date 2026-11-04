package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const (
	maxTaskNameRunes           = 200
	maxTaskMetadataRequestSize = 16 << 10
)

// Error responses the task metadata update API returns to the user. JSON field names
// (name, pinned) stay verbatim; only the human-readable messages are localized.
const (
	errTaskMetaRequestTooLarge = "The request body is too large"
	errTaskMetaNoFields        = "You must provide either name or pinned"
	errTaskMetaNameEmpty       = "The task name cannot be empty"
	// errTaskMetaNameTooLongFmt is a format string filled with the limit by fmt.Sprintf.
	errTaskMetaNameTooLongFmt = "A task name can be at most %d characters"
)

// updateTaskMetadata changes list-only task metadata. It intentionally does not
// touch lifecycle state, scheduling, or the task's immutable description/goal.
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.m.Task(taskID); !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskMetadataRequestSize)
	var request struct {
		Name   *string `json:"name"`
		Pinned *bool   `json:"pinned"`
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, errTaskMetaRequestTooLarge)
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if request.Name == nil && request.Pinned == nil {
		writeErr(w, http.StatusBadRequest, errTaskMetaNoFields)
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, errTaskMetaNameEmpty)
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(trMsg(errTaskMetaNameTooLongFmt), maxTaskNameRunes))
			return
		}
		request.Name = &name
	}
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{Name: request.Name, Pinned: request.Pinned})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}
