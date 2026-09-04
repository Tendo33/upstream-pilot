package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	buildversion "github.com/Tendo33/upstream-pilot/internal/version"
)

const (
	githubRepositoryURL = "https://github.com/Tendo33/upstream-pilot"
	githubLatestURL     = githubRepositoryURL + "/releases/latest"
	githubReleasePrefix = "/Tendo33/upstream-pilot/releases/tag/"
	versionCheckTimeout = 8 * time.Second
	versionSuccessTTL   = 6 * time.Hour
	versionFailureTTL   = 10 * time.Minute
)

type versionStatus struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	RepositoryURL   string    `json:"repository_url"`
	ReleaseURL      string    `json:"release_url,omitempty"`
	Commit          string    `json:"commit"`
	BuildTime       string    `json:"build_time"`
	CheckedAt       time.Time `json:"checked_at"`
}

type versionChecker struct {
	client    *http.Client
	latestURL string
	now       func() time.Time

	mu        sync.Mutex
	cached    versionStatus
	expiresAt time.Time
}

func newVersionChecker(client *http.Client) *versionChecker {
	return &versionChecker{
		client:    client,
		latestURL: githubLatestURL,
		now:       time.Now,
	}
}

func (a *App) versionStatusHandler(w http.ResponseWriter, r *http.Request) error {
	// Release metadata always refers to this project.
	writeData(w, http.StatusOK, versionStatus{CurrentVersion: buildversion.Version, RepositoryURL: githubRepositoryURL, Commit: buildversion.Commit, BuildTime: buildversion.BuildTime, CheckedAt: time.Now().UTC()})
	return nil
}

func (c *versionChecker) status(ctx context.Context) (versionStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now().UTC()
	if !c.expiresAt.IsZero() && now.Before(c.expiresAt) {
		return c.cached, nil
	}

	status := versionStatus{
		CurrentVersion: strings.TrimSpace(buildversion.Version),
		RepositoryURL:  githubRepositoryURL,
		Commit:         strings.TrimSpace(buildversion.Commit),
		BuildTime:      strings.TrimSpace(buildversion.BuildTime),
		CheckedAt:      now,
	}
	if status.CurrentVersion == "" {
		status.CurrentVersion = "dev"
	}

	requestCtx, cancel := context.WithTimeout(ctx, versionCheckTimeout)
	defer cancel()
	latest, releaseURL, err := fetchLatestRelease(requestCtx, c.client, c.latestURL)
	if err != nil {
		c.cached = status
		c.expiresAt = now.Add(versionFailureTTL)
		return status, err
	}

	status.LatestVersion = latest
	status.ReleaseURL = releaseURL
	status.UpdateAvailable = newerSemanticVersion(status.CurrentVersion, latest)
	c.cached = status
	c.expiresAt = now.Add(versionSuccessTTL)
	return status, nil
}

func fetchLatestRelease(ctx context.Context, client *http.Client, latestURL string) (string, string, error) {
	if client == nil {
		return "", "", errors.New("HTTP client is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, latestURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create GitHub release request: %w", err)
	}
	request.Header.Set("Accept", "text/html")
	request.Header.Set("User-Agent", "Upstream Pilot/"+strings.TrimSpace(buildversion.Version))

	response, err := client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("check GitHub release: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return "", "", fmt.Errorf("check GitHub release: unexpected HTTP status %d", response.StatusCode)
	}

	location := response.Header.Get("Location")
	tag, releaseURL, err := parseGitHubReleaseLocation(location)
	if err != nil {
		return "", "", fmt.Errorf("check GitHub release: %w", err)
	}
	return tag, releaseURL, nil
}

func parseGitHubReleaseLocation(location string) (string, string, error) {
	releaseURL, err := url.Parse(strings.TrimSpace(location))
	if err != nil || releaseURL.Scheme != "https" || !strings.EqualFold(releaseURL.Hostname(), "github.com") {
		return "", "", errors.New("invalid release redirect URL")
	}
	if releaseURL.Port() != "" || releaseURL.RawQuery != "" || releaseURL.Fragment != "" {
		return "", "", errors.New("invalid release redirect URL")
	}
	tag, found := strings.CutPrefix(releaseURL.Path, githubReleasePrefix)
	if !found || tag == "" || strings.Contains(tag, "/") {
		return "", "", errors.New("release redirect does not contain a valid tag")
	}
	decodedTag, err := url.PathUnescape(tag)
	if err != nil || decodedTag == "" || strings.Contains(decodedTag, "/") {
		return "", "", errors.New("release redirect contains an invalid tag")
	}
	releaseURL.RawQuery = ""
	releaseURL.Fragment = ""
	return decodedTag, releaseURL.String(), nil
}

func newerSemanticVersion(current, latest string) bool {
	current = canonicalSemanticVersion(current)
	latest = canonicalSemanticVersion(latest)
	return semver.IsValid(current) && semver.IsValid(latest) && semver.Compare(latest, current) > 0
}

func canonicalSemanticVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if value[0] != 'v' {
		value = "v" + value
	}
	return value
}
