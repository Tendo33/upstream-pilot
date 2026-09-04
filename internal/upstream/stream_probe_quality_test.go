package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeMeasuresContentSeparatelyAndRejectsLateFailure(t *testing.T) {
	cases := []struct {
		name, tail string
		success    bool
	}{
		{"complete", "data: {\"type\":\"test_complete\",\"success\":true}\n\n", true},
		{"truncated", "", false},
		{"late-error", "data: {\"type\":\"test_complete\",\"success\":true}\n\ndata: {\"type\":\"error\",\"error\":\"503\"}\n\n", false},
		{"error-then-success", "data: {\"type\":\"error\",\"error\":\"no balance\"}\n\ndata: {\"type\":\"test_complete\",\"success\":true}\n\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"test_start\",\"model\":\"actual-model\"}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
				fmt.Fprint(w, "data: {\"type\":\"content\",\"text\":\"ok\"}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(40 * time.Millisecond)
				fmt.Fprint(w, tc.tail)
			}))
			defer server.Close()
			client, _ := NewSub2Client(server.URL, "test-key", server.Client())
			result, err := client.TestAccount(context.Background(), 7, "requested-model")
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.success || result.StreamComplete != tc.success || result.FirstContentMS == nil || result.DurationMS-*result.FirstContentMS < 25 || result.Model != "requested-model" || result.ActualModel != "actual-model" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}
func TestProbeTimeoutCannotBecomeSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content\",\"text\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := NewSub2Client(server.URL, "test-key", server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	result, err := client.TestAccount(ctx, 7, "test")
	if err == nil || result.Success || result.StreamComplete || result.FirstContentMS == nil {
		t.Fatalf("timeout=%+v %v", result, err)
	}
}
func TestProbeResponseSizeIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+strings.Repeat("x", maxResponseBytes+10))
	}))
	defer server.Close()
	client, _ := NewSub2Client(server.URL, "test-key", server.Client())
	result, err := client.TestAccount(context.Background(), 7, "test")
	if err == nil || result.Success {
		t.Fatalf("size limit: %+v %v", result, err)
	}
}
