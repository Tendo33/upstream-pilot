package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateGroupRateMultiplier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/admin/groups/42" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "secret" {
			t.Fatal("missing API key")
		}
		var input GroupUpdate
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.RateMultiplier == nil || *input.RateMultiplier != 0.72 {
			t.Fatalf("unexpected update %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"id": 42, "name": "Claude", "rate_multiplier": 0.72}})
	}))
	defer server.Close()

	client, err := NewSub2Client(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	rate := 0.72
	group, err := client.UpdateGroup(context.Background(), 42, GroupUpdate{RateMultiplier: &rate})
	if err != nil {
		t.Fatal(err)
	}
	if group.RateMultiplier == nil || *group.RateMultiplier != rate {
		t.Fatalf("unexpected group %#v", group)
	}
}

func TestUpdateGroupRejectsInvalidInputAndResponse(t *testing.T) {
	client := &Sub2Client{}
	if _, err := client.UpdateGroup(context.Background(), 0, GroupUpdate{}); err == nil {
		t.Fatal("expected invalid group ID")
	}
	rate := 0.0
	if _, err := client.UpdateGroup(context.Background(), 1, GroupUpdate{RateMultiplier: &rate}); err == nil {
		t.Fatal("expected invalid rate")
	}
}
