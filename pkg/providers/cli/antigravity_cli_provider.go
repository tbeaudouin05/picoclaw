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

// Chat executes a single agy stream-json turn and returns its terminal result.
func (p *AntigravityCliProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	return p.ChatStreamEvents(ctx, messages, tools, model, options, nil)
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
	Status   string              `json:"status"`
	Response string              `json:"response"`
	Error    string              `json:"error"`
	Usage    antigravityCliUsage `json:"usage"`
}

type antigravityCliUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type antigravityCliStreamRecord struct {
	Event      string `json:"event"`
	StepUpdate struct {
		TextDelta string `json:"text_delta"`
	} `json:"step_update"`
	Result *antigravityCliJSONResponse `json:"result"`
}

func (p *AntigravityCliProvider) parseResponse(output string, tools []ToolDefinition) (*LLMResponse, error) {
	var result antigravityCliJSONResponse
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse antigravity cli response: %w", err)
	}
	return p.parseJSONResponse(result, tools)
}

// ChatStream emits accumulated text as agy produces step updates, then returns
// the parsed terminal result for tool-call handling.
func (p *AntigravityCliProvider) ChatStream(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
	onChunk func(accumulated string),
) (*LLMResponse, error) {
	return p.ChatStreamEvents(ctx, messages, tools, model, options, func(chunk StreamChunk) {
		if onChunk != nil && chunk.Content != "" {
			onChunk(chunk.Content)
		}
	})
}

// ChatStreamEvents reads agy's stream-json NDJSON protocol. Only step_update
// text deltas are emitted before the terminal result; the result itself is
// emitted as a fallback only when no text delta was received.
func (p *AntigravityCliProvider) ChatStreamEvents(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	prompt := p.buildPrompt(messages, tools)
	prepared, mediaDir, cleanup, err := prepareCLIImageInputs(prompt)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	prompt = prepared[0]
	request, err := antigravityCLIRequest(prompt)
	if err != nil {
		return nil, err
	}
	args := p.args(model, mediaDir)

	cmd := exec.CommandContext(ctx, p.command, args...)
	if p.workspace != "" {
		cmd.Dir = p.workspace
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create antigravity cli stdout pipe: %w", err)
	}
	// agy's stream-json input protocol accepts one NDJSON user event per turn.
	// Using stdin keeps arbitrarily large prompts out of argv.
	cmd.Stdin = bytes.NewReader(request)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := isolation.Start(cmd); err != nil {
		return nil, fmt.Errorf("antigravity cli error: %w", err)
	}
	terminateAndWait := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	var content strings.Builder
	var rawOutput strings.Builder
	gotDelta := false
	var terminalResult *antigravityCliJSONResponse
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		rawOutput.Write(scanner.Bytes())
		rawOutput.WriteByte('\n')
		var record antigravityCliStreamRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			terminateAndWait()
			return nil, fmt.Errorf("failed to parse antigravity cli stream record: %w", err)
		}
		switch record.Event {
		case "step_update":
			if record.StepUpdate.TextDelta == "" {
				continue
			}
			gotDelta = true
			content.WriteString(record.StepUpdate.TextDelta)
			if onChunk != nil {
				onChunk(StreamChunk{Content: content.String()})
			}
		case "result":
			if record.Result == nil {
				terminateAndWait()
				return nil, fmt.Errorf("antigravity cli stream result missing result payload")
			}
			terminalResult = record.Result
		}
	}
	if err := scanner.Err(); err != nil {
		terminateAndWait()
		return nil, fmt.Errorf("failed to read antigravity cli stream: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, antigravityCLIExecutionError(err, rawOutput.String(), stderr.String())
	}
	if terminalResult == nil {
		return nil, fmt.Errorf("antigravity cli stream ended without terminal result")
	}
	// agy can put the final text entirely in step updates and leave the
	// successful terminal response empty. Those updates are a valid response;
	// only a response with neither terminal text nor deltas is retryable.
	if gotDelta && strings.TrimSpace(terminalResult.Response) == "" {
		terminalResult.Response = content.String()
	}

	response, err := p.parseJSONResponse(*terminalResult, tools)
	if err != nil {
		return nil, err
	}
	if gotDelta && response.Content == "" && len(response.ToolCalls) == 0 {
		response.Content = content.String()
	}
	if !gotDelta && response.Content != "" && onChunk != nil {
		onChunk(StreamChunk{Content: response.Content})
	}
	return response, nil
}

// antigravityCLIExecutionError preserves the CLI's stderr when it provides a
// classified failure. If stderr is empty, preserve both the original process
// error and raw stdout so callers can diagnose otherwise-unclassified exits.
func antigravityCLIExecutionError(err error, stdout, stderr string) error {
	if stderrText := strings.TrimSpace(stderr); stderrText != "" {
		return fmt.Errorf("antigravity cli error: %s", stderrText)
	}
	if stdoutText := strings.TrimSpace(stdout); stdoutText != "" {
		return fmt.Errorf("antigravity cli unclassified error: %w\nraw output: %s", err, stdoutText)
	}
	return fmt.Errorf("antigravity cli unclassified error: %w", err)
}

func (p *AntigravityCliProvider) args(model string, extraDirs ...string) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--sandbox",
		"--disable-slash-commands",
	}
	args = appendAddDirs(args, append([]string{p.workspace}, extraDirs...)...)
	if model != "" && model != "antigravity-cli" {
		args = append(args, "--model", model)
	}
	return args
}

// antigravityCLIRequest encodes exactly one stream-json user event, including
// its newline NDJSON delimiter.
func antigravityCLIRequest(prompt string) ([]byte, error) {
	request := struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}{Event: "user"}
	request.Message.Content = prompt
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to encode antigravity cli request: %w", err)
	}
	return append(encoded, '\n'), nil
}

func (p *AntigravityCliProvider) parseJSONResponse(result antigravityCliJSONResponse, tools []ToolDefinition) (*LLMResponse, error) {
	if result.Status != "SUCCESS" {
		errText := strings.TrimSpace(result.Error)
		if errText == "" {
			errText = strings.TrimSpace(result.Response)
		}
		if errText == "" {
			errText = "unknown error"
		}
		return nil, fmt.Errorf("antigravity cli returned %s: %s", result.Status, errText)
	}

	toolCalls := filterAntigravityTerminalToolCalls(extractTerminalToolCallsFromText(result.Response), tools)
	content := result.Response
	if strings.TrimSpace(content) == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("antigravity cli returned an empty response")
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		content = ""
		finishReason = "tool_calls"
	}

	var usage *UsageInfo
	if result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0 || result.Usage.TotalTokens > 0 {
		totalTokens := result.Usage.TotalTokens
		if totalTokens == 0 {
			totalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
		}
		usage = &UsageInfo{
			PromptTokens: result.Usage.InputTokens, CompletionTokens: result.Usage.OutputTokens,
			TotalTokens: totalTokens,
		}
	}
	return &LLMResponse{
		Content: strings.TrimSpace(content), ToolCalls: toolCalls, FinishReason: finishReason, Usage: usage,
	}, nil
}

// filterAntigravityTerminalToolCalls marks terminal-protocol calls that were
// not advertised for this request as non-executable. ToolLoop returns the
// reason to agy on its next iteration without consulting ToolRegistry.
func filterAntigravityTerminalToolCalls(toolCalls []ToolCall, tools []ToolDefinition) []ToolCall {
	for i := range toolCalls {
		if !isAdvertisedPicoClawTool(toolCalls[i].Name, tools) {
			toolCalls[i].NonExecutableReason = fmt.Sprintf(
				"requested tool %q is not available for this request; use only advertised PicoClaw tools",
				toolCalls[i].Name,
			)
		}
	}
	return toolCalls
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
