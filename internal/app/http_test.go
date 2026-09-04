package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRequiresJSONContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantCode    string
	}{
		{name: "json", contentType: "application/json"},
		{name: "json with charset", contentType: "application/json; charset=utf-8"},
		{name: "plain text", contentType: "text/plain", wantCode: "JSON_CONTENT_TYPE_REQUIRED"},
		{name: "missing", wantCode: "JSON_CONTENT_TYPE_REQUIRED"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"value":"ok"}`))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			var target struct {
				Value string `json:"value"`
			}

			err := decodeJSON(request, &target)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("decodeJSON() error = %v", err)
				}
				if target.Value != "ok" {
					t.Fatalf("decoded value = %q", target.Value)
				}
				return
			}
			apiErr, ok := err.(*apiError)
			if !ok || apiErr.Code != test.wantCode {
				t.Fatalf("decodeJSON() error = %#v, want code %q", err, test.wantCode)
			}
		})
	}
}

func TestSecurityHeadersDisableAPICaching(t *testing.T) {
	application := &App{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	application.securityHeaders(next).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/sites", nil))

	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
