package app

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestClientIPUsesSocketAddress(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	request.RemoteAddr = "203.0.113.10:54321"
	request.Header.Set("X-Forwarded-For", "198.51.100.4")
	request.Header.Set("X-Real-IP", "198.51.100.5")

	if got := clientIP(request); got != "203.0.113.10" {
		t.Fatalf("clientIP() = %q, want socket peer address", got)
	}
}

func TestVerifyCSRF(t *testing.T) {
	const token = "test-csrf-token"
	hash := sha256.Sum256([]byte(token))
	application := &App{}

	tests := []struct {
		name       string
		method     string
		header     string
		wantStatus int
		wantCalled bool
	}{
		{name: "valid unsafe request", method: http.MethodPost, header: token, wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "missing token", method: http.MethodPost, wantStatus: http.StatusForbidden},
		{name: "wrong token", method: http.MethodPatch, header: "wrong", wantStatus: http.StatusForbidden},
		{name: "safe method", method: http.MethodGet, wantStatus: http.StatusNoContent, wantCalled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			request := httptest.NewRequest(test.method, "/api/v1/test", nil)
			request = request.WithContext(withIdentity(request.Context(), Identity{CSRFHash: hash[:]}))
			if test.header != "" {
				request.Header.Set("X-CSRF-Token", test.header)
			}
			response := httptest.NewRecorder()

			application.verifyCSRF(next).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if called != test.wantCalled {
				t.Fatalf("next called = %v, want %v", called, test.wantCalled)
			}
		})
	}
}

func TestPasswordHashSupportsConfiguredLength(t *testing.T) {
	password := strings.Repeat("密", 128)
	if err := validatePassword(password); err != nil {
		t.Fatalf("128-character password rejected: %v", err)
	}
	if err := validatePassword(password + "a"); err == nil {
		t.Fatal("129-character password must be rejected")
	}
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if !strings.HasPrefix(hash, passwordHashPrefix) {
		t.Fatalf("hash is missing format prefix: %q", hash)
	}
	if err := verifyPassword(hash, password); err != nil {
		t.Fatalf("verifyPassword() error = %v", err)
	}
	if err := verifyPassword(hash, password+"wrong"); err == nil {
		t.Fatal("verifyPassword() accepted the wrong password")
	}
}

func TestVerifyPasswordAcceptsLegacyBcryptHash(t *testing.T) {
	const password = "legacy-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPassword(string(hash), password); err != nil {
		t.Fatalf("legacy hash rejected: %v", err)
	}
}
