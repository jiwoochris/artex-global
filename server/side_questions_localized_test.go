package server

import "testing"

// TestSideQuestionAPIErrorsLocalized guards F21: every user-facing side-question
// message surfaced by the HTTP wrapper (writeErr responses) and by the
// exchange status (e.Error) is its exact English source text. These reach the chat
// side-question panel alongside the sidequestion package errors, so both layers are
// pinned here to avoid a mixed-language panel. Reuses assertEnglishMessage.
func TestSideQuestionAPIErrorsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"ctx_not_saved", sideErrCtxNotSaved, "The side-question context has not been saved yet. Please try again."},
		{"model_config_changed", sideErrModelConfigChanged, "The model configuration was deleted or changed. Run the main agent first to refresh the context."},
		{"service_unavailable", sideErrServiceUnavailable, "The side-question service is unavailable."},
		{"task_archived", sideErrTaskArchived, "The task has been archived or is being deleted."},
		{"worker_deleted", sideErrWorkerDeleted, "The Worker has been deleted."},
		{"bad_question", sideErrBadQuestion, "A question must be 1-4000 characters and must include a valid request ID."},
		{"task_archiving", sideErrTaskArchiving, "The task is being archived or deleted."},
		{"request_id_reused", sideErrRequestIDReused, "The same request ID cannot be used for different questions."},
		{"no_snapshot", sideErrNoSnapshot, "There is no context snapshot yet. Run the main agent first."},
		{"concurrency_limit", sideErrConcurrencyLimit, "Side-question requests have reached the concurrency limit. Please try again shortly."},
		{"answer_stopped", sideErrAnswerStopped, "The answer was stopped."},
		{"answer_timeout", sideErrAnswerTimeout, "The side-question answer was stopped after exceeding 120 seconds."},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}
