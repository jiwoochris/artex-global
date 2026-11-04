package server

import (
	"net/http"
	"strings"
	"time"
)

// maxRequestBytes caps the body of the non-multipart (JSON) requests that reach
// the API. The largest body any route accepts on purpose is 2 MiB
// (maxCompanyMutationBodyBytes, server/assets.go), so 4 MiB is a global ceiling
// that every per-route MaxBytesReader still tightens. Multipart uploads are exempt:
// they carry their own bounded reader with a deliberately larger limit (20 MiB
// skills, 128 MiB chat attachments, 512 MiB work-dir upload).
const maxRequestBytes = 4 << 20

// errRequestBodyTooLarge is the 413 message; the same English text is already in
// errCatalog (Spanish overlay included), so the response follows the output language.
const errRequestBodyTooLarge = "The request body is too large"

// limitRequestBodies refuses an oversized API request before any handler parses
// it, and caps every other body with MaxBytesReader so a lying Content-Length
// cannot stream past the limit. Decoding a capped body that exceeds the limit
// fails the handler's own decode (400); an honest Content-Length is rejected here
// with 413.
func limitRequestBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead ||
			strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > maxRequestBytes {
			writeErr(w, http.StatusRequestEntityTooLarge, errRequestBodyTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		next.ServeHTTP(w, r)
	})
}

// streamsExemptFromWriteTimeout clears the http.Server write deadline on the SSE
// routes: their response stays open for hours (log tail, task activity, side
// questions, update progress) while WriteTimeout is applied once per request and
// would otherwise cut them off. Only streaming gets the exemption — the slow
// non-streaming handlers (custom tool runs, scope-sentry sync, web-search probes)
// keep the deadline and must finish inside it.
func streamsExemptFromWriteTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStreamPath(r.URL.Path) {
			// Best effort: a ResponseWriter without deadline support (httptest) is fine.
			_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		}
		next.ServeHTTP(w, r)
	})
}
