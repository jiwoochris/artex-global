package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression guard for the English source text of the auth response messages. Two
// paths that run without a DB verify it: ① the requireAuth middleware does not go through pg, so
// the real HTTP response body is checked, and ② the remaining handler messages are named
// constants, so the constants are compared with their exact English source text
// (assertEnglishMessage).

// TestRequireAuthMessagesLocalized verifies through the real HTTP handler that the 401 response
// body requireAuth returns for a missing or invalid token is the exact English message. This middleware does not use the database, so it runs to completion without one.
func TestRequireAuthMessagesLocalized(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("k", 32))}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.requireAuth(next)

	cases := []struct {
		name   string
		bearer string
		want   string
	}{
		{"no token", "", "Authentication is required"},
		{"invalid token", "Bearer not-a-valid-token", "The token is invalid or has expired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
			if c.bearer != "" {
				r.Header.Set("Authorization", c.bearer)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status code = %d, want 401", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, c.want) {
				t.Errorf("response body does not contain %q: %s", c.want, body)
			}
			assertEnglishMessage(t, c.name, decodeErrorField(t, rec.Body.Bytes()), c.want)
		})
	}
}

// TestAuthErrorConstantsLocalized asserts that every user-facing message constant the auth
// handlers use is its exact English source text. It catches regressions without a DB even for
// handlers requireAuth cannot exercise directly (= those going through pg).
func TestAuthErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ name, msg, wantEnglish string }{
		{"authErrUnauthorized", authErrUnauthorized, "Authentication is required"},
		{"authErrTokenInvalid", authErrTokenInvalid, "The token is invalid or has expired"},
		{"authErrPasswordAlreadySet", authErrPasswordAlreadySet, "A password has already been set"},
		{"authErrPasswordEmpty", authErrPasswordEmpty, "Enter a password"},
		{"authErrNewPasswordEmpty", authErrNewPasswordEmpty, "Enter a new password"},
		{"authErrPasswordHash", authErrPasswordHash, "Failed to encrypt the password"},
		{"authErrSaveFailedPrefix", authErrSaveFailedPrefix, "Failed to save: "},
		{"authErrTokenGen", authErrTokenGen, "Failed to generate a token"},
		{"authErrBadRequest", authErrBadRequest, "The request format is invalid"},
		{"authErrPasswordNotInit", authErrPasswordNotInit, "The password has not been initialized. Set a password first"},
		{"authErrCurrentPasswordWrong", authErrCurrentPasswordWrong, "The current password is incorrect"},
		{"authErrBadCredential", authErrBadCredential, "Incorrect username or password"},
		{"authErrPasswordTooShort", authErrPasswordTooShort, "The password must be at least 8 characters"},
		{"authErrSetupTokenInvalid", authErrSetupTokenInvalid, "The setup token is incorrect. Enter the setup token printed on the [auth] line of the server console log"},
		{"authErrTooManyAttempts", authErrTooManyAttempts, "Too many failed attempts. Try again later"},
		{"authErrPasswordTooLong", authErrPasswordTooLong, "The password must not exceed 72 bytes"},
		{"authErrDataSourceUnavailable", authErrDataSourceUnavailable, "The data source is temporarily unavailable. Try again shortly"},
	} {
		assertEnglishMessage(t, c.name, c.msg, c.wantEnglish)
	}
}
