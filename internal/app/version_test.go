package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseGitHubReleaseLocation(t *testing.T) {
	tag, releaseURL, err := parseGitHubReleaseLocation("https://github.com/langrenjh-alt/S2AM-GO/releases/tag/v0.3.0")
	if err != nil {
		t.Fatalf("parseGitHubReleaseLocation() error = %v", err)
	}
	if tag != "v0.3.0" {
		t.Fatalf("tag = %q, want v0.3.0", tag)
	}
	if releaseURL != "https://github.com/langrenjh-alt/S2AM-GO/releases/tag/v0.3.0" {
		t.Fatalf("release URL = %q", releaseURL)
	}
}

func TestParseGitHubReleaseLocationRejectsUnexpectedRepository(t *testing.T) {
	locations := []string{
		"https://example.com/langrenjh-alt/S2AM-GO/releases/tag/v0.3.0",
		"https://github.com/other/project/releases/tag/v0.3.0",
		"https://github.com/langrenjh-alt/S2AM-GO/releases/latest",
	}
	for _, location := range locations {
		if _, _, err := parseGitHubReleaseLocation(location); err == nil {
			t.Fatalf("parseGitHubReleaseLocation(%q) accepted an invalid location", location)
		}
	}
}

func TestNewerSemanticVersion(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "new release", current: "v0.2.0", latest: "v0.3.0", want: true},
		{name: "prefix optional", current: "0.2.0", latest: "v0.3.0", want: true},
		{name: "same version", current: "v0.2.0", latest: "v0.2.0"},
		{name: "older release", current: "v0.3.0", latest: "v0.2.0"},
		{name: "development build", current: "dev", latest: "v0.3.0"},
		{name: "commit build", current: "7d5b6c4", latest: "v0.3.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := newerSemanticVersion(test.current, test.latest); got != test.want {
				t.Fatalf("newerSemanticVersion(%q, %q) = %v, want %v", test.current, test.latest, got, test.want)
			}
		})
	}
}

func TestFetchLatestReleaseFromRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		w.Header().Set("Location", "https://github.com/langrenjh-alt/S2AM-GO/releases/tag/v0.3.0")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	tag, releaseURL, err := fetchLatestRelease(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("fetchLatestRelease() error = %v", err)
	}
	if tag != "v0.3.0" || releaseURL != "https://github.com/langrenjh-alt/S2AM-GO/releases/tag/v0.3.0" {
		t.Fatalf("fetchLatestRelease() = %q, %q", tag, releaseURL)
	}
}

func TestVersionCheckerDegradesWhenGitHubFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	checker := newVersionChecker(server.Client())
	checker.latestURL = server.URL

	status, err := checker.status(context.Background())
	if err == nil {
		t.Fatal("status() error = nil, want GitHub failure")
	}
	if status.CurrentVersion == "" || status.RepositoryURL != githubRepositoryURL {
		t.Fatalf("status() did not preserve local version information: %#v", status)
	}
	if status.UpdateAvailable || status.LatestVersion != "" || status.ReleaseURL != "" {
		t.Fatalf("status() reported an update after GitHub failure: %#v", status)
	}
}

func TestVersionStatusHandlerReturnsLocalVersionWhenGitHubFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	checker := newVersionChecker(server.Client())
	checker.latestURL = server.URL
	application := &App{versions: checker}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	response := httptest.NewRecorder()

	if err := application.versionStatusHandler(response, request); err != nil {
		t.Fatalf("versionStatusHandler() error = %v", err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var payload struct {
		Data versionStatus `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.CurrentVersion == "" || payload.Data.RepositoryURL != githubRepositoryURL {
		t.Fatalf("response did not include local version information: %#v", payload.Data)
	}
}
