package cliprovider

import "testing"

func TestLegacyExtractorRemainsPermissiveWhileStrictClassifierRejectsMalformedArguments(t *testing.T) {
	text := `{"tool_calls":[{"id":"call_bad","type":"function","function":{"name":"cron","arguments":"{not-json}"}}]}`

	legacyCalls := extractToolCallsFromText(text)
	if len(legacyCalls) != 1 || legacyCalls[0].Name != "cron" || legacyCalls[0].Arguments != nil {
		t.Fatalf("legacy extractor calls = %#v, want one call with nil parsed arguments", legacyCalls)
	}

	strict := classifyTextProtocolToolCalls(text)
	if len(strict.ToolCalls) != 1 || strict.ToolCalls[0].NonExecutableReason != invalidTextProtocolToolCallReason {
		t.Fatalf("strict classification = %#v, want one non-executable protocol error", strict)
	}
}

func TestStrictClassifierRejectsMalformedPrecursorBeforeValidTerminalCall(t *testing.T) {
	valid := `{"tool_calls":[{"id":"call_ok","type":"function","function":{"name":"cron","arguments":"{}"}}]}`
	classification := classifyTextProtocolToolCalls(`{"tool_calls":[}` + "\n" + valid)

	if len(classification.ToolCalls) != 1 || classification.ToolCalls[0].NonExecutableReason != invalidTextProtocolToolCallReason {
		t.Fatalf("classification = %#v, want one non-executable protocol error", classification)
	}
}

func TestStrictClassifierRejectsMalformedToolCallsKeyTokenWithoutColon(t *testing.T) {
	classification := classifyTextProtocolToolCalls(`{"tool_calls" [}`)

	if len(classification.ToolCalls) != 1 || classification.ToolCalls[0].NonExecutableReason != invalidTextProtocolToolCallReason {
		t.Fatalf("classification = %#v, want one non-executable protocol error", classification)
	}
}

func TestStrictClassifierPreservesOrdinaryJSONToolCallsStringValue(t *testing.T) {
	text := `{"topic":"tool_calls","enabled":true}`
	classification := classifyTextProtocolToolCalls(text)

	if classification.Content != text || len(classification.ToolCalls) != 0 {
		t.Fatalf("classification = %#v, want ordinary JSON preserved as text", classification)
	}
}
