package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func canaryFixture(p CanarySpec) string {
	args := `{"echo":"OK"}`
	encode := func(v any) string { raw, _ := json.Marshal(v); return string(raw) }
	event := func(v any) string { return "data: " + encode(v) + "\n\n" }
	usage := map[string]any{"input_tokens": 20, "output_tokens": 8}
	switch p.Protocol {
	case "chat":
		message := map[string]any{"content": "OK"}
		reason := "stop"
		if p.Tools {
			message = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"name": "pilot_ping", "arguments": args}}}}
			reason = "tool_calls"
		}
		if p.Stream {
			return event(map[string]any{"model": p.Model, "choices": []any{map[string]any{"delta": message}}}) + event(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": reason}}, "usage": usage}) + "data: [DONE]\n\n"
		}
		return encode(map[string]any{"model": p.Model, "choices": []any{map[string]any{"message": message, "finish_reason": reason}}, "usage": usage})
	case "responses":
		output := map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "OK"}}}
		if p.Tools {
			output = map[string]any{"type": "function_call", "id": "tool-1", "name": "pilot_ping", "arguments": args}
		}
		if p.Stream {
			body := event(map[string]any{"type": "response.output_text.delta", "delta": "OK"})
			if p.Tools {
				body = event(map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_call", "id": "tool-1", "name": "pilot_ping"}}) + event(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "tool-1", "delta": args})
			}
			return body + event(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "model": p.Model, "usage": usage}})
		}
		return encode(map[string]any{"status": "completed", "model": p.Model, "output": []any{output}, "usage": usage})
	default:
		block := map[string]any{"type": "text", "text": "OK"}
		reason := "end_turn"
		if p.Tools {
			block = map[string]any{"type": "tool_use", "id": "tool-1", "name": "pilot_ping", "input": map[string]any{"echo": "OK"}}
			reason = "tool_use"
		}
		if p.Stream {
			body := event(map[string]any{"type": "message_start", "message": map[string]any{"model": p.Model, "usage": usage}})
			if p.Tools {
				body += event(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "name": "pilot_ping"}}) + event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}})
			} else {
				body += event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "OK"}})
			}
			return body + event(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": usage}) + event(map[string]any{"type": "message_stop"})
		}
		return encode(map[string]any{"model": p.Model, "content": []any{block}, "stop_reason": reason, "usage": usage})
	}
}

func TestCanaryProtocolProfiles(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, tool := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/tools=%v", protocol, stream, tool), func(t *testing.T) {
					p := CanarySpec{Model: "pilot-test", Protocol: protocol, Stream: stream, Tools: tool, MaxOutputTokens: 64}
					var calls atomic.Int32
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						endpoint, _ := canaryBody(p)
						if r.URL.Path != endpoint {
							t.Errorf("unexpected path %s", r.URL.Path)
						}
						if protocol == "messages" {
							if r.Header.Get("x-api-key") != "test-group-key" {
								t.Error("missing group key")
							}
						} else if r.Header.Get("Authorization") != "Bearer test-group-key" {
							t.Error("missing group key")
						}
						var body CanarySpec
						_ = json.NewDecoder(r.Body).Decode(&body)
						if body.Model != p.Model || body.Stream != stream {
							t.Error("wrong request profile")
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
						} else {
							w.Header().Set("Content-Type", "application/json")
						}
						_, _ = w.Write([]byte(canaryFixture(p)))
					}))
					defer s.Close()
					result, err := ProbeGateway(context.Background(), s.Client(), s.URL, "test-group-key", p)
					if err != nil || !result.Success || !result.Complete || result.ToolValid != tool || result.ActualModel != p.Model || calls.Load() != 1 {
						t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
					}
					if stream != (result.FirstContentMS != nil) {
						t.Fatal("TTFT must measure content in stream only")
					}
					if result.InputTokens == nil || *result.InputTokens != 20 || result.OutputTokens == nil || *result.OutputTokens != 8 {
						t.Fatal("lost usage provenance")
					}
				})
			}
		}
	}
}

func TestCanaryDoesNotRetryOrInventCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"503", `{"error":"no upstream"}`, 503},
		{"partial", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n", 200},
		{"empty", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", 200},
		{"truncated", "data: {\"choices\":[{\"delta\":{\"content\":\"half\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			result, err := ProbeGateway(context.Background(), s.Client(), s.URL, "test", CanarySpec{Model: "test", Protocol: "chat", Stream: true, MaxOutputTokens: 64})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || calls.Load() != 1 {
				t.Fatalf("false healthy or replay: %+v", result)
			}
		})
	}
}

func TestCanaryHeartbeatDoesNotCountAsFirstContent(t *testing.T) {
	p := CanarySpec{Model: "test", Protocol: "chat", Stream: true, MaxOutputTokens: 64}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": heartbeat\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(canaryFixture(p)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := ProbeGateway(ctx, s.Client(), s.URL, "test", p)
	if err != nil || !result.Success || result.FirstContentMS == nil || *result.FirstContentMS < 20 || result.DurationMS >= 900 {
		t.Fatalf("heartbeat or open socket affected measurement: %+v %v", result, err)
	}
}

func TestCanaryRejectsAdditionalToolArguments(t *testing.T) {
	p := CanarySpec{Model: "test", Protocol: "chat", Tools: true, MaxOutputTokens: 64}
	body := strings.ReplaceAll(canaryFixture(p), `\"echo\":\"OK\"`, `\"echo\":\"OK\",\"unexpected\":true`)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer s.Close()
	result, err := ProbeGateway(context.Background(), s.Client(), s.URL, "test", p)
	if err != nil || result.Success || result.ToolValid {
		t.Fatalf("invalid tool accepted: %+v %v", result, err)
	}
}
