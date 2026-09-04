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
	"strconv"
	"strings"
	"time"
)

func (c *Sub2Client) streamAccountTest(ctx context.Context, id int64, model string) (result ProbeResult, resultErr error) {
	if id <= 0 {
		return result, errors.New("Sub2API account ID must be positive")
	}
	started := time.Now()
	result.Model = model
	defer func() {
		result.DurationMS = int(time.Since(started).Milliseconds())
		result.LatencyMS = result.DurationMS
		result.Message = strings.ReplaceAll(result.Message, c.apiKey, "[redacted]")
		result.FailureData = strings.ReplaceAll(result.FailureData, c.apiKey, "[redacted]")
	}()
	body := map[string]string{}
	if model != "" {
		body["model_id"] = model
	}
	payload, _ := json.Marshal(body)
	path := "/accounts/" + strconv.FormatInt(id, 10) + "/test"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return result, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		result.ControlPlaneError = true
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		result.ControlPlaneError = resp.StatusCode == 401 || resp.StatusCode == 403
		status := resp.StatusCode
		result.HTTPStatus = &status
		if readErr != nil {
			return result, readErr
		}
		result.FailureData = compactUntrustedText(string(raw), maxErrorRunes)
		return result, responseError("Sub2API", http.MethodPost, path, resp, raw)
	}
	reader := bufio.NewReader(resp.Body)
	leading, _ := reader.Peek(1)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") || string(leading) == "{" {
		raw, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
		if err != nil {
			return result, err
		}
		if len(raw) > maxResponseBytes {
			return result, errors.New("account test exceeds response limit")
		}
		if err := probeEnvelopeFailure(raw); err != nil {
			return result, err
		}
		result = ParseProbeResponse(raw, 0)
		result.ActualModel = result.Model
		result.Model = model
		result.StreamComplete = result.Success
		return result, nil
	}
	// Parse SSE frames as they arrive. Heartbeats and status events do not count
	// as first content. Memory is bounded even if a peer never sends a terminal.
	scanner := bufio.NewScanner(io.LimitReader(reader, maxResponseBytes+1))
	scanner.Buffer(make([]byte, 4096), maxResponseBytes+1)
	events := []probeEvent{}
	data := []string{}
	consumed := 0
	failed := false
	flush := func() {
		if len(data) == 0 {
			return
		}
		frame := strings.Join(data, "\n")
		data = nil
		if strings.TrimSpace(frame) == "[DONE]" {
			return
		}
		event, err := parseProbeEvent([]byte(frame))
		if err != nil {
			failed = true
			result.Message = "account test returned malformed SSE data"
			return
		}
		events = append(events, event)
		if event.Model != "" && result.ActualModel == "" {
			result.ActualModel = event.Model
		}
		switch event.Type {
		case "content", "image":
			if result.FirstContentMS == nil && (event.Type == "image" || strings.TrimSpace(scalarString(event.OriginalData["text"])) != "") {
				value := int(time.Since(started).Milliseconds())
				result.FirstContentMS = &value
			}
		case "error":
			failed = true
		case "test_complete", "done", "complete":
			if event.Success != nil && *event.Success {
				result.StreamComplete = true
			} else if event.Type != "test_complete" && event.Success == nil && event.Error == "" {
				result.StreamComplete = true
			} else {
				failed = true
			}
		}
		if event.Error != "" || (event.Success != nil && !*event.Success) || (event.HTTPStatus != nil && *event.HTTPStatus >= 400) {
			failed = true
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		consumed += len(line) + 1
		if consumed > maxResponseBytes {
			return result, errors.New("account test exceeds response limit")
		}
		if line == "" || line == "\r" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(line[5:]))
		}
	}
	flush()
	parsed := resultFromProbeEvents(events, 0)
	result.StreamComplete = result.StreamComplete && !failed
	result.Success = parsed.Success && result.StreamComplete && !failed
	result.HTTPStatus = parsed.HTTPStatus
	result.Code = parsed.Code
	result.FailureData = parsed.FailureData
	if result.Message == "" {
		result.Message = parsed.Message
	}
	if failed && parsed.Success {
		result.Message = "account test contained a failure before completion"
	}
	if err := scanner.Err(); err != nil {
		result.Success = false
		result.StreamComplete = false
		return result, fmt.Errorf("read account test: %w", err)
	}
	if !result.StreamComplete && result.Message == "account test succeeded" {
		result.Message = "account test ended without explicit completion"
	}
	return result, nil
}
