// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package cliprovider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildCLIToolsPrompt creates the tool definitions section for a CLI provider system prompt.
func buildCLIToolsPrompt(tools []ToolDefinition) string {
	var sb strings.Builder

	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("Terminal-JSON protocol:\n")
	sb.WriteString("- When calling a tool, emit exactly one JSON object with a tool_calls field. It must be the final non-whitespace content; prose is allowed only before it.\n")
	sb.WriteString("- Do not wrap the JSON object in Markdown fences, inline backticks, or quotation marks.\n")
	sb.WriteString("- Never show JSON unless you are actually calling a tool.\n")
	sb.WriteString("- Call only the functions advertised below. Otherwise, respond in plain text.\n\n")
	sb.WriteString(
		`{"tool_calls":[{"id":"call_xxx","type":"function","function":{"name":"tool_name","arguments":"{...}"}}]}`,
	)
	sb.WriteString("\n\n")
	sb.WriteString("function.arguments MUST be a JSON-encoded string.\n")
	sb.WriteString("Newline escaping: use \\n for a real newline; use \\\\n for a literal backslash followed by n.\n")
	sb.WriteString(
		"Escape quotes and backslashes for the outer JSON string.\n\n",
	)
	sb.WriteString("### Tool Definitions:\n\n")

	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		sb.WriteString(fmt.Sprintf("#### %s\n", tool.Function.Name))
		if tool.Function.Description != "" {
			sb.WriteString(fmt.Sprintf("Description: %s\n", tool.Function.Description))
		}
		if len(tool.Function.Parameters) > 0 {
			paramsJSON, _ := json.Marshal(tool.Function.Parameters)
			sb.WriteString(fmt.Sprintf("Parameters:\n```json\n%s\n```\n", string(paramsJSON)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// NormalizeToolCall normalizes a ToolCall to ensure all fields are properly populated.
// It handles cases where Name/Arguments might be in different locations (top-level vs Function)
// and ensures both are populated consistently.
func NormalizeToolCall(tc ToolCall) ToolCall {
	normalized := tc

	if normalized.ThoughtSignature == "" &&
		normalized.ExtraContent != nil &&
		normalized.ExtraContent.Google != nil {
		normalized.ThoughtSignature = normalized.ExtraContent.Google.ThoughtSignature
	}

	// Ensure Name is populated from Function if not set
	if normalized.Name == "" && normalized.Function != nil {
		normalized.Name = normalized.Function.Name
	}

	// Ensure Arguments is not nil
	if normalized.Arguments == nil {
		normalized.Arguments = map[string]any{}
	}

	// Parse Arguments from Function.Arguments if not already set
	if len(normalized.Arguments) == 0 && normalized.Function != nil && normalized.Function.Arguments != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(normalized.Function.Arguments), &parsed); err == nil && parsed != nil {
			normalized.Arguments = parsed
		}
	}

	// Ensure Function is populated with consistent values
	argsJSON, _ := json.Marshal(normalized.Arguments)
	if normalized.Function == nil {
		normalized.Function = &FunctionCall{
			Name:             normalized.Name,
			Arguments:        string(argsJSON),
			ThoughtSignature: normalized.ThoughtSignature,
		}
	} else {
		if normalized.Function.Name == "" {
			normalized.Function.Name = normalized.Name
		}
		if normalized.Name == "" {
			normalized.Name = normalized.Function.Name
		}
		if normalized.Function.Arguments == "" {
			normalized.Function.Arguments = string(argsJSON)
		}
		if normalized.Function.ThoughtSignature == "" {
			normalized.Function.ThoughtSignature = normalized.ThoughtSignature
		}
		if normalized.ThoughtSignature == "" {
			normalized.ThoughtSignature = normalized.Function.ThoughtSignature
		}
	}

	return normalized
}
