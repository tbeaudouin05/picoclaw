package cliprovider

import (
	"encoding/json"
	"strings"
)

const invalidTextProtocolToolCallReason = "invalid PicoClaw tool-call protocol: do not quote tool calls; emit precisely one valid unfenced JSON object with a tool_calls field as the final non-whitespace content, or respond with ordinary plaintext containing no tool-call JSON"

type textProtocolToolCallClassification struct {
	ToolCalls []ToolCall
	Content   string
}

// classifyTextProtocolToolCalls is the single strict classifier for CLI model
// output that may contain PicoClaw's terminal JSON protocol. A credible but
// invalid attempt becomes a non-executable call so ToolLoop can return precise
// corrective feedback instead of exposing the attempted protocol to the user.
func classifyTextProtocolToolCalls(text string) textProtocolToolCallClassification {
	type candidate struct {
		raw   string
		start int
		end   int
	}

	clueCount := countToolCallsKeyClues(text)
	var candidates []candidate
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(text[i:]))
		var object map[string]json.RawMessage
		if err := decoder.Decode(&object); err != nil {
			continue
		}
		toolCalls, ok := object["tool_calls"]
		if !ok || toolCalls == nil {
			continue
		}
		end := i + int(decoder.InputOffset())
		candidates = append(candidates, candidate{raw: text[i:end], start: i, end: end})
		i = end - 1
	}

	if len(candidates) == 1 && clueCount == 1 {
		candidate := candidates[0]
		calls := extractStrictToolCallsFromJSON(candidate.raw)
		if len(calls) > 0 && strings.TrimSpace(text[candidate.end:]) == "" &&
			!candidateIsCodeFormattedOrQuoted(text, candidate.start) {
			return textProtocolToolCallClassification{
				ToolCalls: calls,
				Content:   strings.TrimSpace(text[:candidate.start]),
			}
		}
	}

	if clueCount > 0 {
		return textProtocolToolCallClassification{ToolCalls: []ToolCall{{
			ID:                  "picoclaw_text_protocol_error",
			Type:                "function",
			Name:                "picoclaw_text_protocol_error",
			Arguments:           map[string]any{},
			NonExecutableReason: invalidTextProtocolToolCallReason,
			Function: &FunctionCall{
				Name:      "picoclaw_text_protocol_error",
				Arguments: "{}",
			},
		}}}
	}

	return textProtocolToolCallClassification{Content: text}
}

func countToolCallsKeyClues(text string) int {
	const key = `"tool_calls"`
	count := 0
	for offset := 0; ; {
		index := strings.Index(text[offset:], key)
		if index == -1 {
			return count
		}
		start := offset + index
		before := start - 1
		for before >= 0 && strings.ContainsRune(" \t\r\n", rune(text[before])) {
			before--
		}
		// A quoted tool_calls token in an object-key position is a credible
		// protocol clue even when the attempted key/value separator is missing.
		// Requiring an object-key delimiter before it avoids treating ordinary
		// JSON string values as protocol attempts.
		if before >= 0 && (text[before] == '{' || text[before] == ',') {
			count++
		}
		offset = start + len(key)
	}
}

func candidateIsCodeFormattedOrQuoted(text string, start int) bool {
	prefix := text[:start]
	trimmed := strings.TrimRight(prefix, " \t\r\n")
	if strings.HasSuffix(trimmed, "`") || strings.HasSuffix(trimmed, `"`) || strings.HasSuffix(trimmed, `'`) {
		return true
	}
	// A fenced block normally has a language marker and newline between its
	// opening fence and the object, so inspect fence parity as well.
	return strings.Count(prefix, "```")%2 == 1
}

// extractToolCallsFromText retains the legacy Codex CLI extraction behavior.
// Claude and Antigravity use classifyTextProtocolToolCalls directly.
func extractToolCallsFromText(text string) []ToolCall {
	start := strings.Index(text, `{"tool_calls"`)
	if start == -1 {
		return nil
	}
	end := findMatchingBrace(text, start)
	if end == start {
		return nil
	}
	return extractToolCallsFromJSON(text[start:end])
}

// extractToolCallsFromJSON retains the legacy permissive extraction behavior
// used by Codex. In particular, malformed function.arguments still produces a
// call with nil parsed arguments, as it did before strict classification.
func extractToolCallsFromJSON(jsonStr string) []ToolCall {
	var wrapper struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &wrapper); err != nil {
		return nil
	}

	var result []ToolCall
	for _, tc := range wrapper.ToolCalls {
		var args map[string]any
		json.Unmarshal([]byte(tc.Function.Arguments), &args)
		result = append(result, ToolCall{
			ID: tc.ID, Type: tc.Type, Name: tc.Function.Name, Arguments: args,
			Function: &FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return result
}

// extractStrictToolCallsFromJSON validates one terminal text-protocol object.
func extractStrictToolCallsFromJSON(jsonStr string) []ToolCall {
	var wrapper struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &wrapper); err != nil || len(wrapper.ToolCalls) == 0 {
		return nil
	}

	result := make([]ToolCall, 0, len(wrapper.ToolCalls))
	for _, tc := range wrapper.ToolCalls {
		if tc.ID == "" || tc.Type != "function" || tc.Function.Name == "" || tc.Function.Arguments == "" {
			return nil
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil || args == nil {
			return nil
		}
		result = append(result, ToolCall{
			ID: tc.ID, Type: tc.Type, Name: tc.Function.Name, Arguments: args,
			Function: &FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return result
}

// stripToolCallsFromText retains the legacy Codex CLI stripping behavior.
func stripToolCallsFromText(text string) string {
	start := strings.Index(text, `{"tool_calls"`)
	if start == -1 {
		return text
	}
	end := findMatchingBrace(text, start)
	if end == start {
		return text
	}
	return strings.TrimSpace(text[:start] + text[end:])
}
