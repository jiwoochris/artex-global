package server

import "net/http"

// contentSecurityPolicy is the policy for the statically exported UI served by
// webuiHandler(). The export is pre-built and baked into the binary, so it cannot
// carry per-response nonces or a build-time script hash; it ships inline <script>
// (the theme-boot snippet and Turbopack's chunk manifest) and React writes inline
// style attributes, so 'unsafe-inline' in script-src/style-src is the strictest
// policy that does not break the UI. Everything that can be locked down is:
// no remote origin is allowed, and object/plugin embedding, framing, base-uri
// rewriting and cross-origin form posts are all refused.
//
// shortcut: script-src keeps 'unsafe-inline' because the export cannot be
// nonced; switch to a hash/nonce policy if the frontend build ever emits one.
// connect-src 'self' assumes the UI and the API are served from one origin (the
// embedded build is); a split deployment that points NEXT_PUBLIC_SSE_BASE at
// another host must relax connect-src in its own front-end proxy, which is where
// that UI's headers are set anyway.
const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// securityHeaders sets the HTTP hardening headers on every response — API, UI and
// errors alike — and is the outermost handler, so nothing can bypass it. Without
// it there is no CSP, no framing protection (clickjacking the tool-approval
// prompt) and no sniffing protection on the report exports, whose content is
// derived from the LLM and from scanned targets.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}
