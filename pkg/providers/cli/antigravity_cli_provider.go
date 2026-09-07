package cliprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sipeed/picoclaw/pkg/isolation"
)

// AntigravityCliProvider implements LLMProvider using the local agy CLI.
// It is intentionally separate from the direct OAuth antigravity provider.
type AntigravityCliProvider struct {
	command   string
	workspace string
}

// NewAntigravityCliProvider creates a local Antigravity CLI provider.
func NewAntigravityCliProvider(workspace string) *AntigravityCliProvider {
	return &AntigravityCliProvider{command: "agy", workspace: workspace}
}

// Chat executes agy in its documented sandboxed, noninteractive plan mode.
func (p *AntigravityCliProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	prompt := p.buildPrompt(messages, tools)
	args := []string{
		"--print=" + prompt,
		"--output-format", "json",
		"--sandbox",
		"--mode", "plan",
		"--disable-slash-commands",
	}
	if p.workspace != "" {
		args = append(args, "--add-dir", p.workspace)
	}
	if model != "" && model != "antigravity-cli" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, p.command, args...)
	if p.workspace != "" {
		cmd.Dir = p.workspace
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := isolation.Run(cmd); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return nil, fmt.Errorf("antigravity cli error: %s", stderrText)
		}
		return nil, fmt.Errorf("antigravity cli error: %w", err)
	}

	return p.parseResponse(stdout.String(), tools)
}

// GetDefaultModel returns the provider's model sentinel.
func (p *AntigravityCliProvider) GetDefaultModel() string {
	return "antigravity-cli"
}

func (p *AntigravityCliProvider) buildPrompt(messages []Message, tools []ToolDefinition) string {
	var systemParts, conversationParts []string
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user":
			conversationParts = append(conversationParts, "User: "+msg.Content)
		case "assistant":
			conversationParts = append(conversationParts, "Assistant: "+msg.Content)
		case "tool":
			conversationParts = append(conversationParts,
				fmt.Sprintf("[Tool Result for %s]: %s", msg.ToolCallID, msg.Content))
		}
	}

	if len(tools) > 0 {
		systemParts = append(systemParts,
			"PicoClaw-provided tools use a terminal JSON text protocol and are separate from Antigravity-native tools. "+
				"To call a PicoClaw tool, emit it only in the final response using the JSON object format below. "+
				"Never use or represent an Antigravity-native tool call as a PicoClaw tool call. Only call PicoClaw "+
				"functions advertised below for this request.\n\n"+buildCLIToolsPrompt(tools))
	}

	var parts []string
	if len(systemParts) > 0 {
		parts = append(parts, "## System Instructions\n\n"+strings.Join(systemParts, "\n\n"))
	}
	if len(conversationParts) > 0 {
		parts = append(parts, "## Conversation\n\n"+strings.Join(conversationParts, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

type antigravityCliJSONResponse struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *AntigravityCliProvider) parseResponse(output string, tools []ToolDefinition) (*LLMResponse, error) {
	var result antigravityCliJSONResponse
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse antigravity cli response: %w", err)
	}
	if result.IsError {
		return nil, fmt.Errorf("antigravity cli returned error: %s", result.Result)
	}

	toolCalls := filterPicoClawToolCalls(extractTerminalToolCallsFromText(result.Result), tools)
	content := result.Result
	finishReason := "stop"
	if len(toolCalls) > 0 {
		content = ""
		finishReason = "tool_calls"
	}

	var usage *UsageInfo
	if result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0 {
		usage = &UsageInfo{
			PromptTokens: result.Usage.InputTokens, CompletionTokens: result.Usage.OutputTokens,
			TotalTokens: result.Usage.InputTokens + result.Usage.OutputTokens,
		}
	}
	return &LLMResponse{
		Content: strings.TrimSpace(content), ToolCalls: toolCalls, FinishReason: finishReason, Usage: usage,
	}, nil
}

// extractTerminalToolCallsFromText accepts a text-protocol call only when the
// whole trimmed response is one valid tool_calls JSON object. Unlike the shared
// extractor, it deliberately does not search prose for embedded JSON: agy
// result text is executable only when it follows the terminal protocol exactly.
func extractTerminalToolCallsFromText(text string) []ToolCall {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return nil
	}

	var wrapper struct {
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil || wrapper.ToolCalls == nil {
		return nil
	}

	return extractToolCallsFromJSON(trimmed)
}
