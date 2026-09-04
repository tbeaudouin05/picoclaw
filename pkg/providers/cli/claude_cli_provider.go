package cliprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sipeed/picoclaw/pkg/isolation"
)

// ClaudeCliProvider implements LLMProvider using the claude CLI as a subprocess.
type ClaudeCliProvider struct {
	command   string
	workspace string
}

// NewClaudeCliProvider creates a new Claude CLI provider.
func NewClaudeCliProvider(workspace string) *ClaudeCliProvider {
	return &ClaudeCliProvider{
		command:   "claude",
		workspace: workspace,
	}
}

// Chat implements LLMProvider.Chat by executing the claude CLI.
func (p *ClaudeCliProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	systemPrompt := p.buildSystemPrompt(messages, tools)
	prompt := p.messagesToPrompt(messages)

	// Claude CLI rejects --dangerously-skip-permissions when PicoClaw runs as root.
	// Root-safe permission allowlists are configured narrowly in /root/.claude/settings.json.
	args := []string{"-p", "--output-format", "json", "--no-chrome"}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	if model != "" && model != "claude-code" {
		args = append(args, "--model", model)
	}
	args = append(args, "-") // read from stdin

	cmd := exec.CommandContext(ctx, p.command, args...)
	if p.workspace != "" {
		cmd.Dir = p.workspace
	}
	cmd.Stdin = bytes.NewReader([]byte(prompt))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute the CLI through the shared isolation wrapper so external provider
	// processes honor the configured isolation policy.
	if err := isolation.Run(cmd); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		stdoutStr := strings.TrimSpace(stdout.String())
		switch {
		case stderrStr != "" && stdoutStr != "":
			return nil, fmt.Errorf("claude cli error: %w\nstderr: %s\nstdout: %s", err, stderrStr, stdoutStr)
		case stderrStr != "":
			return nil, fmt.Errorf("claude cli error: %s", stderrStr)
		case stdoutStr != "":
			return nil, fmt.Errorf("claude cli error: %w\noutput: %s", err, stdoutStr)
		default:
			return nil, fmt.Errorf("claude cli error: %w", err)
		}
	}

	return p.parseClaudeCliResponse(stdout.String())
}

// ChatStream streams accumulated text from the Claude CLI while returning the
// complete response after its terminal result record.
func (p *ClaudeCliProvider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(accumulated string),
) (*LLMResponse, error) {
	return p.ChatStreamEvents(ctx, messages, tools, model, options, func(chunk StreamChunk) {
		if onChunk != nil && chunk.Content != "" {
			onChunk(chunk.Content)
		}
	})
}

// ChatStreamEvents streams text deltas and completed native tool calls from the
// Claude CLI stream-json output. Thinking is intentionally not exposed.
func (p *ClaudeCliProvider) ChatStreamEvents(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	systemPrompt := p.buildSystemPrompt(messages, tools)
	prompt := p.messagesToPrompt(messages)

	args := []string{"-p", "--verbose", "--output-format", "stream-json", "--include-partial-messages", "--no-chrome"}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	if model != "" && model != "claude-code" {
		args = append(args, "--model", model)
	}
	args = append(args, "-")

	cmd := exec.CommandContext(ctx, p.command, args...)
	if p.workspace != "" {
		cmd.Dir = p.workspace
	}
	cmd.Stdin = bytes.NewReader([]byte(prompt))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create claude cli stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := isolation.Start(cmd); err != nil {
		return nil, fmt.Errorf("claude cli error: %w", err)
	}
	terminateAndWait := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	var content strings.Builder
	var terminalResult *claudeCliJSONResponse
	type toolUseAccum struct {
		id   string
		name string
		args strings.Builder
	}
	activeToolUses := make(map[int]*toolUseAccum)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record claudeCliStreamRecord
		if err := json.Unmarshal(line, &record); err != nil {
			terminateAndWait()
			return nil, fmt.Errorf("failed to parse claude cli stream record: %w", err)
		}

		if record.Type == "result" {
			var result claudeCliJSONResponse
			if err := json.Unmarshal(line, &result); err != nil {
				terminateAndWait()
				return nil, fmt.Errorf("failed to parse claude cli stream result: %w", err)
			}
			terminalResult = &result
			continue
		}

		if record.Type != "stream_event" {
			continue
		}

		switch record.Event.Type {
		case "content_block_start":
			if record.Event.ContentBlock.Type == "tool_use" {
				// A block index is unique for a streamed message. Replacing any
				// stale entry avoids mixing arguments if a malformed stream reuses it.
				activeToolUses[record.Event.Index] = &toolUseAccum{
					id:   record.Event.ContentBlock.ID,
					name: record.Event.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			if record.Event.Delta.Type == "text_delta" && record.Event.Delta.Text != "" {
				content.WriteString(record.Event.Delta.Text)
				if onChunk != nil {
					onChunk(StreamChunk{Content: content.String()})
				}
				continue
			}
			if record.Event.Delta.Type == "input_json_delta" && record.Event.Delta.PartialJSON != "" {
				if toolUse, ok := activeToolUses[record.Event.Index]; ok {
					toolUse.args.WriteString(record.Event.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			toolUse, ok := activeToolUses[record.Event.Index]
			if !ok {
				continue
			}
			delete(activeToolUses, record.Event.Index)
			if toolUse.id == "" || toolUse.name == "" {
				continue
			}

			argsJSON := toolUse.args.String()
			args := make(map[string]any)
			if argsJSON != "" {
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					continue
				}
			}
			if onChunk != nil {
				onChunk(StreamChunk{ToolCalls: []ToolCall{{
					ID:        toolUse.id,
					Type:      "function",
					Name:      toolUse.name,
					Arguments: args,
					Function: &FunctionCall{
						Name:      toolUse.name,
						Arguments: argsJSON,
					},
				}}})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		terminateAndWait()
		return nil, fmt.Errorf("failed to read claude cli stream: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("claude cli error: %s", stderrStr)
		}
		return nil, fmt.Errorf("claude cli error: %w", err)
	}
	if terminalResult == nil {
		return nil, fmt.Errorf("claude cli stream ended without terminal result")
	}

	return p.parseClaudeCliJSONResponse(*terminalResult)
}

// GetDefaultModel returns the default model identifier.
func (p *ClaudeCliProvider) GetDefaultModel() string {
	return "claude-code"
}

// messagesToPrompt converts messages to a CLI-compatible prompt string.
func (p *ClaudeCliProvider) messagesToPrompt(messages []Message) string {
	var parts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// handled via --system-prompt flag
		case "user":
			parts = append(parts, "User: "+msg.Content)
		case "assistant":
			parts = append(parts, "Assistant: "+msg.Content)
		case "tool":
			parts = append(parts, fmt.Sprintf("[Tool Result for %s]: %s", msg.ToolCallID, msg.Content))
		}
	}

	// Simplify single user message
	if len(parts) == 1 && strings.HasPrefix(parts[0], "User: ") {
		return strings.TrimPrefix(parts[0], "User: ")
	}

	return strings.Join(parts, "\n")
}

// buildSystemPrompt combines system messages and tool definitions.
func (p *ClaudeCliProvider) buildSystemPrompt(messages []Message, tools []ToolDefinition) string {
	var parts []string

	for _, msg := range messages {
		if msg.Role == "system" {
			parts = append(parts, msg.Content)
		}
	}

	if len(tools) > 0 {
		parts = append(parts, buildCLIToolsPrompt(tools))
	}

	return strings.Join(parts, "\n\n")
}

// parseClaudeCliResponse parses the JSON output from the claude CLI.
func (p *ClaudeCliProvider) parseClaudeCliResponse(output string) (*LLMResponse, error) {
	var resp claudeCliJSONResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse claude cli response: %w", err)
	}
	return p.parseClaudeCliJSONResponse(resp)
}

func (p *ClaudeCliProvider) parseClaudeCliJSONResponse(resp claudeCliJSONResponse) (*LLMResponse, error) {
	if resp.IsError {
		return nil, fmt.Errorf("claude cli returned error: %s", resp.Result)
	}

	toolCalls := p.extractToolCalls(resp.Result)

	finishReason := "stop"
	content := resp.Result
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		content = p.stripToolCallsJSON(resp.Result)
	}

	var usage *UsageInfo
	if resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0 {
		usage = &UsageInfo{
			PromptTokens:     resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.OutputTokens,
		}
	}

	return &LLMResponse{
		Content:      strings.TrimSpace(content),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        usage,
	}, nil
}

// extractToolCalls delegates to the shared extractToolCallsFromText function.
func (p *ClaudeCliProvider) extractToolCalls(text string) []ToolCall {
	return extractToolCallsFromText(text)
}

// stripToolCallsJSON delegates to the shared stripToolCallsFromText function.
func (p *ClaudeCliProvider) stripToolCallsJSON(text string) string {
	return stripToolCallsFromText(text)
}

// findMatchingBrace finds the index after the closing brace matching the opening brace at pos.
func findMatchingBrace(text string, pos int) int {
	depth := 0
	for i := pos; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return pos
}

// claudeCliJSONResponse represents the JSON output from the claude CLI.
// Matches the real claude CLI v2.x output format.
type claudeCliJSONResponse struct {
	Type         string             `json:"type"`
	Subtype      string             `json:"subtype"`
	IsError      bool               `json:"is_error"`
	Result       string             `json:"result"`
	SessionID    string             `json:"session_id"`
	TotalCostUSD float64            `json:"total_cost_usd"`
	DurationMS   int                `json:"duration_ms"`
	DurationAPI  int                `json:"duration_api_ms"`
	NumTurns     int                `json:"num_turns"`
	Usage        claudeCliUsageInfo `json:"usage"`
}

// claudeCliUsageInfo represents token usage from the claude CLI response.
type claudeCliUsageInfo struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type claudeCliStreamRecord struct {
	Type  string `json:"type"`
	Event struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	} `json:"event"`
}
