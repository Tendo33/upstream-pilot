package upstream

import (
	"strings"
	"testing"
)

func TestParseProbeResponse(t *testing.T) {
	result := ParseProbeResponse([]byte("data: {\"type\":\"content\"}\n\ndata: {\"type\":\"done\",\"model\":\"gpt-test\",\"latency_ms\":42}\n\n"), 100)
	if !result.Success || result.Model != "gpt-test" || result.LatencyMS != 42 {
		t.Fatalf("unexpected result: %#v", result)
	}
	failed := ParseProbeResponse([]byte(`{"success":false,"message":"quota exhausted"}`), 5)
	if failed.Success || failed.Message != "quota exhausted" {
		t.Fatalf("unexpected failed result: %#v", failed)
	}
}

func TestParseProbeResponseSub2APITerminalEvent(t *testing.T) {
	raw := "data: {\"type\":\"test_start\",\"model\":\"gpt-5.4\"}\r\n\r\n" +
		"data: {\"type\":\"content\",\"text\":\"ok\"}\r\n\r\n" +
		"data: {\"type\":\"test_complete\",\"success\":true}\r\n\r\n"
	result := ParseProbeResponse([]byte(raw), 17)
	if !result.Success || result.Model != "gpt-5.4" || result.Message != "account test succeeded" {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseProbeResponseExplicitFailureWinsOverTerminalType(t *testing.T) {
	for _, raw := range []string{
		`{"type":"complete","success":false,"message":"failed explicitly"}`,
		"data: {\"type\":\"test_complete\",\"success\":false,\"error\":\"upstream rejected\"}\n\n",
	} {
		result := ParseProbeResponse([]byte(raw), 2)
		if result.Success {
			t.Fatalf("explicit failure parsed as success: %#v", result)
		}
	}
}

func TestParseProbeResponseEnvelope(t *testing.T) {
	result := ParseProbeResponse([]byte(`{"code":0,"message":"success","data":{"success":true,"message":"ok","model":"test"}}`), 8)
	if !result.Success || result.Message != "ok" || result.Model != "test" {
		t.Fatalf("result = %#v", result)
	}
	rejected := ParseProbeResponse([]byte(`{"code":422,"message":"invalid model"}`), 8)
	if rejected.Success || rejected.Message == "" {
		t.Fatalf("rejected = %#v", rejected)
	}
}

func TestParseProbeResponseMalformedPayload(t *testing.T) {
	result := ParseProbeResponse([]byte(`{"success":`), -10)
	if result.Success || result.LatencyMS != 0 || result.Message == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseProbeResponsePreservesNestedFailureEvidence(t *testing.T) {
	raw := "data: {\"type\":\"error\",\"status\":\"failed\",\"error\":{\"status\":403,\"code\":\"INSUFFICIENT_BALANCE\",\"message\":\"Insufficient account balance\"}}\n\n" +
		"data: {\"type\":\"test_complete\",\"status\":\"failed\",\"success\":false}\n\n"
	result := ParseProbeResponse([]byte(raw), 25)
	if result.Success || result.Message != "Insufficient account balance" {
		t.Fatalf("result = %#v", result)
	}
	if result.HTTPStatus == nil || *result.HTTPStatus != 403 {
		t.Fatalf("HTTPStatus = %v", result.HTTPStatus)
	}
	if result.Code != "INSUFFICIENT_BALANCE" {
		t.Fatalf("Code = %q", result.Code)
	}
	if !strings.Contains(result.FailureData, `"error":{"code":"INSUFFICIENT_BALANCE"`) {
		t.Fatalf("FailureData did not preserve nested error: %s", result.FailureData)
	}
}

func TestParseProbeResponseExtractsNumericCodeAsHTTPStatus(t *testing.T) {
	result := ParseProbeResponse([]byte(`{"type":"error","success":false,"code":429,"error":{"message":"Too many requests"}}`), 4)
	if result.Success || result.HTTPStatus == nil || *result.HTTPStatus != 429 || result.Code != "429" {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseProbeResponseTreatsNestedErrorWithoutTypeAsFailure(t *testing.T) {
	result := ParseProbeResponse([]byte(`{"error":{"statusCode":403,"message":"Forbidden"}}`), 6)
	if result.Success || result.Message != "Forbidden" || result.HTTPStatus == nil || *result.HTTPStatus != 403 {
		t.Fatalf("result = %#v", result)
	}
}
