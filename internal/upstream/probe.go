package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const maxProbeFailureDataRunes = 2000

type probeEvent struct {
	Type         string
	Status       string
	Success      *bool
	Message      string
	Error        string
	Model        string
	LatencyMS    int
	HTTPStatus   *int
	Code         string
	OriginalData map[string]any
}

func ParseProbeResponse(raw []byte, elapsedMS int) ProbeResult {
	if elapsedMS < 0 {
		elapsedMS = 0
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff"))
	if strings.HasPrefix(trimmed, "{") {
		event, err := parseProbeEvent([]byte(trimmed))
		if err != nil {
			if errors.Is(err, errInvalidProbeJSON) {
				return ProbeResult{LatencyMS: elapsedMS, Message: "account test returned invalid JSON"}
			}
			return ProbeResult{LatencyMS: elapsedMS, Message: compactUntrustedText(err.Error(), maxErrorRunes)}
		}
		return resultFromProbeEvents([]probeEvent{event}, elapsedMS)
	}

	events := make([]probeEvent, 0)
	var lines []string
	flush := func() {
		if len(lines) == 0 {
			return
		}
		value := strings.TrimSpace(strings.Join(lines, "\n"))
		lines = nil
		if value == "" || value == "[DONE]" {
			return
		}
		event, err := parseProbeEvent([]byte(value))
		if err == nil {
			events = append(events, event)
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return resultFromProbeEvents(events, elapsedMS)
}

var errInvalidProbeJSON = errors.New("invalid probe JSON")

type probeEnvelopeError struct {
	Code    int
	Message string
	Detail  string
}

func (e *probeEnvelopeError) Error() string {
	message := e.Message
	if message == "" {
		message = "request rejected"
	}
	return fmt.Sprintf("upstream rejected request (code %d): %s", e.Code, message)
}

func (e *probeEnvelopeError) StatusCode() int { return e.Code }

func (e *probeEnvelopeError) FailureDetail() string { return e.Detail }

func probeEnvelopeFailure(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var object map[string]any
	if decoder.Decode(&object) != nil {
		return nil
	}
	code, ok := integerValue(object["code"])
	if !ok || code == 0 || code > 1_000_000 || code < -1_000_000 {
		return nil
	}
	return &probeEnvelopeError{
		Code:    int(code),
		Message: messageFromValue(object["message"]),
		Detail:  compactProbeJSON(object),
	}
}

func parseProbeEvent(raw []byte) (probeEvent, error) {
	if !json.Valid(bytes.TrimSpace(raw)) {
		return probeEvent{}, errInvalidProbeJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var original map[string]any
	if err := decoder.Decode(&original); err != nil || original == nil {
		return probeEvent{}, errInvalidProbeJSON
	}

	active := original
	activeNested := false
	if nested, ok := original["data"].(map[string]any); ok && !hasProbeTerminalFields(original) {
		active = nested
		activeNested = true
	}
	codeValue, hasCode := original["code"]
	if hasCode {
		if code, ok := integerValue(codeValue); ok {
			if code == 0 {
				if nested, ok := original["data"].(map[string]any); ok {
					active = nested
					activeNested = true
				}
			} else {
				failed := false
				active = cloneMap(original)
				activeNested = false
				active["success"] = failed
				if _, exists := active["status"]; !exists {
					active["status"] = "failed"
				}
			}
		}
	}

	event := probeEvent{
		Type:         scalarString(active["type"]),
		Status:       scalarString(active["status"]),
		Message:      messageFromValue(active["message"]),
		Error:        messageFromValue(active["error"]),
		Model:        scalarString(active["model"]),
		LatencyMS:    positiveInt(active["latency_ms"]),
		HTTPStatus:   probeHTTPStatus(original),
		Code:         probeCode(original),
		OriginalData: original,
	}
	if success, ok := active["success"].(bool); ok {
		event.Success = &success
	}
	if event.Message == "" && activeNested {
		event.Message = messageFromValue(original["message"])
	}
	if event.Error == "" && activeNested {
		event.Error = messageFromValue(original["error"])
	}
	return event, nil
}

func resultFromProbeEvents(events []probeEvent, elapsedMS int) ProbeResult {
	result := ProbeResult{LatencyMS: elapsedMS, Message: "account test did not return a successful terminal event"}
	for _, event := range events {
		if event.Model != "" && result.Model == "" {
			result.Model = compactUntrustedText(event.Model, 256)
		}
		if event.LatencyMS > 0 {
			result.LatencyMS = event.LatencyMS
		}
	}

	terminalIndex := -1
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		typeName := strings.ToLower(strings.TrimSpace(event.Type))
		status := strings.ToLower(strings.TrimSpace(event.Status))
		if event.Success != nil || event.Error != "" || (event.HTTPStatus != nil && *event.HTTPStatus >= 400) || typeName == "test_complete" || typeName == "done" || typeName == "complete" || typeName == "error" || terminalProbeStatus(status) {
			terminalIndex = i
			break
		}
	}
	if terminalIndex < 0 {
		if len(events) > 0 {
			result.FailureData = failureDataFromEvents(events)
		}
		return result
	}

	terminal := events[terminalIndex]
	typeName := strings.ToLower(strings.TrimSpace(terminal.Type))
	status := strings.ToLower(strings.TrimSpace(terminal.Status))
	switch {
	case typeName == "error":
		result.Success = false
	case terminal.Success != nil:
		result.Success = *terminal.Success
	case successfulProbeStatus(status):
		result.Success = true
	case failedProbeStatus(status), typeName == "test_complete":
		result.Success = false
	default:
		// Legacy streams commonly use done/complete without a success field.
		result.Success = typeName == "done" || typeName == "complete"
	}

	if result.Success {
		if terminal.Message != "" {
			result.Message = compactUntrustedText(terminal.Message, maxErrorRunes)
		} else {
			result.Message = "account test succeeded"
		}
		return result
	}

	evidence := terminal
	bestScore := probeEvidenceScore(terminal)
	for i := len(events) - 1; i >= 0; i-- {
		candidate := events[i]
		if score := probeEvidenceScore(candidate); score > bestScore {
			evidence = candidate
			bestScore = score
		}
	}
	if evidence.Error != "" {
		result.Message = compactUntrustedText(evidence.Error, maxErrorRunes)
	} else if evidence.Message != "" {
		result.Message = compactUntrustedText(evidence.Message, maxErrorRunes)
	} else if terminal.Error != "" {
		result.Message = compactUntrustedText(terminal.Error, maxErrorRunes)
	} else if terminal.Message != "" {
		result.Message = compactUntrustedText(terminal.Message, maxErrorRunes)
	} else {
		result.Message = "account test failed"
	}
	result.HTTPStatus = evidence.HTTPStatus
	if result.HTTPStatus == nil {
		result.HTTPStatus = terminal.HTTPStatus
	}
	result.Code = evidence.Code
	if result.Code == "" {
		result.Code = terminal.Code
	}
	result.FailureData = failureDataFromEvents(events)
	return result
}

func probeEvidenceScore(event probeEvent) int {
	score := 0
	if event.Message != "" {
		score++
	}
	if event.Code != "" {
		score += 2
	}
	if event.HTTPStatus != nil {
		score += 3
	}
	if event.Error != "" {
		score += 4
	}
	return score
}

func failureDataFromEvents(events []probeEvent) string {
	values := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if event.OriginalData != nil && relevantFailureEvent(event) {
			values = append(values, event.OriginalData)
		}
	}
	if len(values) == 0 {
		return ""
	}
	if len(values) == 1 {
		return compactProbeJSON(values[0])
	}
	return compactProbeJSON(values)
}

func relevantFailureEvent(event probeEvent) bool {
	typeName := strings.ToLower(strings.TrimSpace(event.Type))
	status := strings.ToLower(strings.TrimSpace(event.Status))
	return event.Error != "" || event.Message != "" || event.Code != "" || event.HTTPStatus != nil || event.Success != nil ||
		typeName == "error" || typeName == "test_complete" || failedProbeStatus(status)
}

func hasProbeTerminalFields(value map[string]any) bool {
	for _, key := range []string{"success", "type", "status", "error"} {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}

func cloneMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value)+1)
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func compactProbeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return compactUntrustedText(string(encoded), maxProbeFailureDataRunes)
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(typed)
	default:
		return ""
	}
}

func messageFromValue(value any) string {
	if value == nil {
		return ""
	}
	if scalar := scalarString(value); scalar != "" {
		return compactUntrustedText(scalar, maxErrorRunes)
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"message", "error", "detail", "body", "data", "cause", "code"} {
			if message := messageFromValue(object[key]); message != "" {
				return message
			}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if message := messageFromValue(object[key]); message != "" {
				return message
			}
		}
	}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if message := messageFromValue(item); message != "" {
				return message
			}
		}
	}
	return ""
}

func positiveInt(value any) int {
	number, ok := integerValue(value)
	if !ok || number <= 0 || number > int64(^uint(0)>>1) {
		return 0
	}
	return int(number)
}

func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		integer, err := typed.Int64()
		return integer, err == nil
	case float64:
		integer := int64(typed)
		return integer, typed == float64(integer)
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case string:
		integer, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return integer, err == nil
	default:
		return 0, false
	}
}

func probeHTTPStatus(value any) *int {
	return probeHTTPStatusDepth(value, 0)
}

func probeHTTPStatusDepth(value any, depth int) *int {
	if value == nil || depth > 5 {
		return nil
	}
	if object, ok := value.(map[string]any); ok {
		for _, wanted := range []string{"http_status", "httpStatus", "status_code", "statusCode", "status", "code"} {
			if raw, exists := object[wanted]; exists {
				if number, ok := integerValue(raw); ok && number >= 100 && number <= 599 {
					status := int(number)
					return &status
				}
			}
		}
		for _, wanted := range []string{"error", "response", "cause", "detail", "body", "data"} {
			if status := probeHTTPStatusDepth(object[wanted], depth+1); status != nil {
				return status
			}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if status := probeHTTPStatusDepth(object[key], depth+1); status != nil {
				return status
			}
		}
	}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if status := probeHTTPStatusDepth(item, depth+1); status != nil {
				return status
			}
		}
	}
	return nil
}

func probeCode(value any) string {
	return probeCodeDepth(value, 0)
}

func probeCodeDepth(value any, depth int) string {
	if value == nil || depth > 5 {
		return ""
	}
	if object, ok := value.(map[string]any); ok {
		if code := scalarString(object["code"]); code != "" && code != "0" {
			return compactUntrustedText(code, 128)
		}
		for _, wanted := range []string{"error", "response", "cause", "detail", "body", "data"} {
			if code := probeCodeDepth(object[wanted], depth+1); code != "" {
				return code
			}
		}
	}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if code := probeCodeDepth(item, depth+1); code != "" {
				return code
			}
		}
	}
	return ""
}

func terminalProbeStatus(status string) bool {
	return successfulProbeStatus(status) || failedProbeStatus(status)
}

func successfulProbeStatus(status string) bool {
	switch status {
	case "ok", "success", "succeeded", "complete", "completed":
		return true
	default:
		return false
	}
}

func failedProbeStatus(status string) bool {
	switch status {
	case "error", "fail", "failed", "failure":
		return true
	default:
		return false
	}
}
