package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CanarySpec describes a synthetic user-entry request. It contains no user
// prompts and tool results are validated as data, never executed.
type CanarySpec struct {
	TraceID         string `json:"-"`
	Model           string `json:"model"`
	Protocol        string `json:"protocol"`
	Stream          bool   `json:"stream"`
	Tools           bool   `json:"tools"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

func (p CanarySpec) Validate() error {
	if strings.TrimSpace(p.Model) == "" || len(p.Model) > 256 {
		return errors.New("请填写有效测试模型")
	}
	if p.Protocol != "chat" && p.Protocol != "responses" && p.Protocol != "messages" {
		return errors.New("协议必须是 chat、responses 或 messages")
	}
	if p.MaxOutputTokens < 16 || p.MaxOutputTokens > 8192 {
		return errors.New("每次输出限制须为 16–8192 tokens")
	}
	return nil
}

type CanaryResult struct {
	ControlError   bool   `json:"control_error"`
	Success        bool   `json:"success"`
	HTTPStatus     int    `json:"http_status"`
	FirstContentMS *int   `json:"first_content_ms"`
	DurationMS     int    `json:"duration_ms"`
	Complete       bool   `json:"complete"`
	ToolValid      bool   `json:"tool_valid"`
	ActualModel    string `json:"actual_model"`
	RequestID      string `json:"request_id"`
	Failure        string `json:"failure"`
	InputTokens    *int   `json:"input_tokens"`
	OutputTokens   *int   `json:"output_tokens"`
}

func canaryBody(p CanarySpec) (string, map[string]any) {
	prompt := "Reply with OK."
	if p.Tools {
		prompt = "Call pilot_ping with echo set to OK."
	}
	params := map[string]any{"type": "object", "properties": map[string]any{"echo": map[string]any{"type": "string", "enum": []string{"OK"}}}, "required": []string{"echo"}, "additionalProperties": false}
	body := map[string]any{"model": p.Model, "stream": p.Stream}
	switch p.Protocol {
	case "responses":
		body["input"] = prompt
		body["max_output_tokens"] = p.MaxOutputTokens
		if p.Tools {
			body["tools"] = []any{map[string]any{"type": "function", "name": "pilot_ping", "description": "Synthetic health check; no actions are executed.", "parameters": params}}
			body["tool_choice"] = map[string]any{"type": "function", "name": "pilot_ping"}
		}
		return "/v1/responses", body
	case "messages":
		body["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
		body["max_tokens"] = p.MaxOutputTokens
		if p.Tools {
			body["tools"] = []any{map[string]any{"name": "pilot_ping", "description": "Synthetic health check; no actions are executed.", "input_schema": params}}
			body["tool_choice"] = map[string]any{"type": "tool", "name": "pilot_ping"}
		}
		return "/v1/messages", body
	default:
		body["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
		body["max_completion_tokens"] = p.MaxOutputTokens
		if p.Tools {
			body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "pilot_ping", "description": "Synthetic health check; no actions are executed.", "parameters": params}}}
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "pilot_ping"}}
		}
		if p.Stream {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
		return "/v1/chat/completions", body
	}
}

// ProbeGateway makes exactly one request. Budget reservations and scheduling
// belong to the caller. No automatic retry can replay a potentially charged POST.
func ProbeGateway(ctx context.Context, client *http.Client, baseURL, key string, p CanarySpec) (result CanaryResult, err error) {
	if err = p.Validate(); err != nil {
		return result, err
	}
	base, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(key) == "" {
		return result, errors.New("缺少分组专用 Key")
	}
	endpoint, body := canaryBody(p)
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/v1")+endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	if p.TraceID != "" {
		request.Header.Set("X-Session-Id", "pilot-canary:"+p.TraceID)
		request.Header.Set("X-Request-ID", p.TraceID)
	}
	if p.Protocol == "messages" {
		request.Header.Set("x-api-key", key)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	started := time.Now()
	defer func() { result.DurationMS = int(time.Since(started).Milliseconds()) }()
	transport := *client
	transport.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(request)
	if err != nil {
		result.Failure = "network_or_timeout"
		return result, err
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	result.RequestID = response.Header.Get("x-request-id")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Failure = fmt.Sprintf("http_%d", response.StatusCode)
		return result, nil
	}
	state := canaryParser{result: &result, spec: p, started: started, tools: map[string]*canaryTool{}}
	reader := &io.LimitedReader{R: response.Body, N: maxResponseBytes + 1}
	if p.Stream {
		if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
			result.Failure = "expected_event_stream"
			return result, nil
		}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), maxResponseBytes)
		var lines []string
		consume := func() error {
			if len(lines) == 0 {
				return nil
			}
			data := strings.Join(lines, "\n")
			lines = nil
			return state.event([]byte(data))
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err = consume(); err != nil || result.Complete || result.Failure != "" {
					break
				}
			} else if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err == nil {
			err = scanner.Err()
		}
		if err == nil {
			err = consume()
		}
	} else {
		var data []byte
		data, err = io.ReadAll(reader)
		if err == nil {
			err = state.document(data)
		}
	}
	if reader.N <= 0 {
		err = errors.New("探测响应超过读取上限")
	}
	if err != nil {
		result.Failure = "invalid_or_interrupted_response"
		return result, err
	}
	if len(state.tools) == 1 {
		for _, tool := range state.tools {
			var args map[string]any
			if tool.Name == "pilot_ping" && json.Unmarshal([]byte(tool.Arguments), &args) == nil && len(args) == 1 && args["echo"] == "OK" {
				result.ToolValid = true
			}
		}
	}
	result.Success = result.Complete && result.Failure == "" && ((!p.Tools && state.content) || p.Tools && result.ToolValid)
	if !result.Success && result.Failure == "" {
		if !result.Complete {
			result.Failure = "incomplete_response"
		} else if p.Tools {
			result.Failure = "invalid_tool_structure"
		} else {
			result.Failure = "empty_content"
		}
	}
	return result, nil
}

type canaryTool struct{ Name, Arguments string }
type canaryParser struct {
	result            *CanaryResult
	spec              CanarySpec
	started           time.Time
	content, finished bool
	tools             map[string]*canaryTool
}

func (p *canaryParser) first() {
	if p.spec.Stream && p.result.FirstContentMS == nil {
		ms := int(time.Since(p.started).Milliseconds())
		p.result.FirstContentMS = &ms
	}
}
func (p *canaryParser) text(v any) {
	if s, ok := v.(string); ok && s != "" {
		p.content = true
		p.first()
	}
}
func (p *canaryParser) tool(id, name, args string) {
	t := p.tools[id]
	if t == nil {
		t = &canaryTool{}
		p.tools[id] = t
	}
	if name != "" {
		t.Name = name
		p.first()
	}
	t.Arguments += args
	if args != "" {
		p.first()
	}
}
func (p *canaryParser) usage(v any) {
	m, _ := v.(map[string]any)
	for key, dest := range map[string]**int{"input_tokens": &p.result.InputTokens, "prompt_tokens": &p.result.InputTokens, "output_tokens": &p.result.OutputTokens, "completion_tokens": &p.result.OutputTokens} {
		if n, ok := m[key].(float64); ok && n >= 0 && n < 1e9 && n == float64(int(n)) {
			x := int(n)
			*dest = &x
		}
	}
}
func str(v any) string         { s, _ := v.(string); return s }
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any        { a, _ := v.([]any); return a }
func (p *canaryParser) event(raw []byte) error {
	if string(raw) == "[DONE]" {
		if p.spec.Protocol == "chat" && p.finished {
			p.result.Complete = true
		}
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if model := str(m["model"]); model != "" {
		p.result.ActualModel = model
	}
	p.usage(m["usage"])
	if m["error"] != nil {
		p.result.Failure = "upstream_error"
		return nil
	}
	switch p.spec.Protocol {
	case "chat":
		for _, v := range array(m["choices"]) {
			choice := obj(v)
			delta := obj(choice["delta"])
			p.text(delta["content"])
			for _, v := range array(delta["tool_calls"]) {
				t := obj(v)
				f := obj(t["function"])
				p.tool(fmt.Sprint(t["index"]), str(f["name"]), str(f["arguments"]))
			}
			switch str(choice["finish_reason"]) {
			case "stop", "tool_calls":
				p.finished = true
			case "length", "content_filter":
				p.result.Failure = "truncated_or_filtered"
			}
		}
	case "responses":
		switch str(m["type"]) {
		case "response.output_text.delta":
			p.text(m["delta"])
		case "response.output_item.added":
			item := obj(m["item"])
			if str(item["type"]) == "function_call" {
				p.tool(str(item["id"]), str(item["name"]), str(item["arguments"]))
			}
		case "response.function_call_arguments.delta":
			p.tool(str(m["item_id"]), "", str(m["delta"]))
		case "response.completed":
			response := obj(m["response"])
			p.result.Complete = str(response["status"]) == "completed"
			p.usage(response["usage"])
			p.result.ActualModel = str(response["model"])
		case "response.failed", "response.incomplete":
			p.result.Failure = "incomplete_response"
		}
	case "messages":
		switch str(m["type"]) {
		case "message_start":
			message := obj(m["message"])
			p.usage(message["usage"])
			p.result.ActualModel = str(message["model"])
		case "content_block_start":
			block := obj(m["content_block"])
			if str(block["type"]) == "tool_use" {
				p.tool(fmt.Sprint(m["index"]), str(block["name"]), "")
			}
			p.text(block["text"])
		case "content_block_delta":
			delta := obj(m["delta"])
			p.text(delta["text"])
			if str(delta["type"]) == "input_json_delta" {
				p.tool(fmt.Sprint(m["index"]), "", str(delta["partial_json"]))
			}
		case "message_delta":
			p.usage(m["usage"])
			reason := str(obj(m["delta"])["stop_reason"])
			if reason == "end_turn" || reason == "tool_use" {
				p.finished = true
			} else if reason != "" {
				p.result.Failure = "truncated_or_filtered"
			}
		case "message_stop":
			p.result.Complete = p.finished
		}
	}
	return nil
}

func (p *canaryParser) document(raw []byte) error {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	p.result.ActualModel = str(m["model"])
	p.usage(m["usage"])
	if m["error"] != nil {
		p.result.Failure = "upstream_error"
		return nil
	}
	switch p.spec.Protocol {
	case "chat":
		for _, v := range array(m["choices"]) {
			c := obj(v)
			message := obj(c["message"])
			p.text(message["content"])
			for i, v := range array(message["tool_calls"]) {
				f := obj(obj(v)["function"])
				p.tool(fmt.Sprint(i), str(f["name"]), str(f["arguments"]))
			}
			reason := str(c["finish_reason"])
			p.result.Complete = reason == "stop" || reason == "tool_calls"
		}
	case "responses":
		p.result.Complete = str(m["status"]) == "completed"
		for i, v := range array(m["output"]) {
			item := obj(v)
			if str(item["type"]) == "function_call" {
				p.tool(fmt.Sprint(i), str(item["name"]), str(item["arguments"]))
			}
			for _, v := range array(item["content"]) {
				p.text(obj(v)["text"])
			}
		}
	case "messages":
		reason := str(m["stop_reason"])
		p.result.Complete = reason == "end_turn" || reason == "tool_use"
		for i, v := range array(m["content"]) {
			block := obj(v)
			p.text(block["text"])
			if str(block["type"]) == "tool_use" {
				raw, _ := json.Marshal(block["input"])
				p.tool(fmt.Sprint(i), str(block["name"]), string(raw))
			}
		}
	}
	return nil
}
