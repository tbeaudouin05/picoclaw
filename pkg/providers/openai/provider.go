// Package openai implements the official OpenAI Responses API provider for
// API-key authenticated model entries.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/responses"

	"github.com/sipeed/picoclaw/pkg/providers/common"
	orc "github.com/sipeed/picoclaw/pkg/providers/openai_responses_common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type (
	ToolCall               = protocoltypes.ToolCall
	LLMResponse            = protocoltypes.LLMResponse
	StreamChunk            = protocoltypes.StreamChunk
	UsageInfo              = protocoltypes.UsageInfo
	ReasoningItem          = protocoltypes.ReasoningItem
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
)

const (
	defaultStreamingReadIdleTimeout = 5 * time.Minute
	responsesAPIPath                = "responses"
)

// Provider sends API-key authenticated requests to the official OpenAI
// Responses API. It deliberately has no response or conversation state: the
// caller supplies the complete input history on every request.
type Provider struct {
	apiKey        string
	apiBase       string
	httpClient    *http.Client
	userAgent     string
	extraBody     map[string]any
	customHeaders map[string]string
}

// Option configures the OpenAI Responses provider.
type Option func(*Provider)

// WithRequestTimeout sets the HTTP request timeout.
func WithRequestTimeout(timeout time.Duration) Option {
	return func(p *Provider) {
		if timeout > 0 {
			p.httpClient.Timeout = timeout
		}
	}
}

// WithUserAgent sets the User-Agent header for requests.
func WithUserAgent(userAgent string) Option {
	return func(p *Provider) {
		p.userAgent = userAgent
	}
}

// WithExtraBody adds fields to every request body. These fields override the
// provider's ordinary defaults except for mandatory stateless replay fields.
func WithExtraBody(extraBody map[string]any) Option {
	return func(p *Provider) {
		p.extraBody = extraBody
	}
}

// WithCustomHeaders adds headers to every request. Custom values are applied
// last so callers can intentionally override built-in headers.
func WithCustomHeaders(customHeaders map[string]string) Option {
	return func(p *Provider) {
		p.customHeaders = customHeaders
	}
}

// NewProvider creates an API-key authenticated OpenAI Responses provider.
// apiBase is normally https://api.openai.com/v1; tests and controlled proxies
// may provide another base that exposes a Responses-compatible endpoint.
func NewProvider(apiKey, apiBase, proxy, userAgent string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:     apiKey,
		apiBase:    strings.TrimRight(apiBase, "/"),
		userAgent:  userAgent,
		httpClient: common.NewHTTPClient(proxy),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	return p
}

// NewProviderWithTimeout creates a provider with model-entry HTTP settings.
func NewProviderWithTimeout(
	apiKey, apiBase, proxy, userAgent string,
	requestTimeoutSeconds int,
	extraBody map[string]any,
	customHeaders map[string]string,
) *Provider {
	return NewProvider(
		apiKey,
		apiBase,
		proxy,
		userAgent,
		WithRequestTimeout(time.Duration(requestTimeoutSeconds)*time.Second),
		WithExtraBody(extraBody),
		WithCustomHeaders(customHeaders),
	)
}

// Chat sends a non-streaming request to POST /responses.
func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	requestBody := p.buildRequestBody(messages, tools, model, options, false)
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := p.newRequest(ctx, jsonData, false)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, common.HandleErrorResponse(resp, p.apiBase)
	}

	return parseProviderResponse(resp.Body, "")
}

// ChatStream implements the provider streaming interface. onChunk receives
// the accumulated visible text after each Responses text delta.
func (p *Provider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(accumulated string),
) (*LLMResponse, error) {
	return p.ChatStreamEvents(
		ctx,
		messages,
		tools,
		model,
		options,
		func(chunk StreamChunk) {
			if onChunk != nil && strings.TrimSpace(chunk.Content) != "" {
				onChunk(chunk.Content)
			}
		},
	)
}

// ChatStreamEvents implements the Responses SSE interface and reports
// accumulated reasoning/text deltas plus completed function calls.
func (p *Provider) ChatStreamEvents(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	requestBody := p.buildRequestBody(messages, tools, model, options, true)
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := p.newRequest(ctx, jsonData, true)
	if err != nil {
		return nil, err
	}

	// A total http.Client timeout would terminate an otherwise healthy long
	// stream. The request context and the idle timeout below remain active.
	streamClient := &http.Client{Transport: p.httpClient.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, common.HandleErrorResponse(resp, p.apiBase)
	}

	return parseStreamResponse(
		ctx,
		withStreamingReadIdleTimeout(resp.Body, defaultStreamingReadIdleTimeout),
		onChunk,
	)
}

func (p *Provider) newRequest(ctx context.Context, jsonData []byte, stream bool) (*http.Request, error) {
	if strings.TrimSpace(p.apiBase) == "" {
		return nil, fmt.Errorf("OpenAI API base not configured")
	}

	requestURL, err := url.JoinPath(p.apiBase, responsesAPIPath)
	if err != nil {
		return nil, fmt.Errorf("failed to build OpenAI request URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	for key, value := range p.customHeaders {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	return req, nil
}

func (p *Provider) buildRequestBody(
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	stream bool,
) map[string]any {
	input, instructions := orc.TranslateMessages(messages)
	requestBody := map[string]any{
		"model": model,
		"input": input,
	}

	if instructions != "" {
		requestBody["instructions"] = instructions
	}

	nativeSearch, _ := options["native_search"].(bool)
	if len(tools) > 0 || nativeSearch {
		requestBody["tools"] = orc.TranslateTools(tools, nativeSearch)
		requestBody["tool_choice"] = "auto"
	}

	if maxTokens, ok := common.AsInt(options["max_tokens"]); ok {
		requestBody["max_output_tokens"] = maxTokens
	} else if maxTokens, ok := common.AsInt(options["max_output_tokens"]); ok {
		requestBody["max_output_tokens"] = maxTokens
	}
	if temperature, ok := common.AsFloat(options["temperature"]); ok {
		requestBody["temperature"] = temperature
	}
	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		requestBody["prompt_cache_key"] = cacheKey
	}
	applyReasoningOptions(requestBody, options, model)

	// Configured extra fields intentionally have the same precedence as the
	// OpenAI-compatible provider. The two statelessness invariants and model
	// parameter compatibility are applied after the merge.
	maps.Copy(requestBody, p.extraBody)
	if isReasoningModel(model) && reasoningEffort(requestBody["reasoning"]) != "none" {
		delete(requestBody, "temperature")
	}
	// Clone configured includes so repeated requests never mutate extraBody.
	var includes []any
	switch configured := requestBody["include"].(type) {
	case []string:
		for _, value := range configured {
			includes = append(includes, value)
		}
	case []any:
		includes = append(includes, configured...)
	}
	found := false
	for _, value := range includes {
		if entry, ok := value.(string); ok && entry == "reasoning.encrypted_content" {
			found = true
			break
		}
	}
	if !found {
		includes = append(includes, "reasoning.encrypted_content")
	}
	requestBody["include"] = includes
	requestBody["store"] = false
	if stream {
		requestBody["stream"] = true
	}

	return requestBody
}

func isReasoningModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	for _, prefix := range []string{"gpt-5", "gpt-6", "o1", "o3", "o4"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func reasoningEffort(raw any) string {
	reasoning, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	effort, _ := reasoning["effort"].(string)
	return strings.ToLower(strings.TrimSpace(effort))
}

func applyReasoningOptions(requestBody map[string]any, options map[string]any, model string) {
	// Explicit reasoning options and extra_body retain caller precedence.
	// Only generated thinking controls are constrained by model capabilities.
	if raw, ok := options["reasoning"].(map[string]any); ok {
		requestBody["reasoning"] = maps.Clone(raw)
	}

	if effort, ok := options["reasoning_effort"].(string); ok && strings.TrimSpace(effort) != "" {
		setReasoningField(requestBody, "effort", strings.ToLower(strings.TrimSpace(effort)))
	}

	level, _ := options["thinking_level"].(string)
	if effort := generatedReasoningEffort(model, level); effort != "" {
		setReasoningField(requestBody, "effort", effort)
	}

	if summary, ok := options["reasoning_summary"].(string); ok && strings.TrimSpace(summary) != "" {
		setReasoningField(requestBody, "summary", summary)
	}
}

// generatedReasoningEffort deliberately uses conservative family-compatible
// values. Off selects a supported effort when reasoning cannot be disabled.
func generatedReasoningEffort(model, level string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	if !isReasoningModel(model) || strings.Contains(model, "chat") || strings.Contains(model, "search") ||
		strings.HasPrefix(model, "o1-mini") || strings.HasPrefix(model, "o1-preview") {
		return ""
	}
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "off", "low", "medium", "high", "xhigh":
	default:
		return ""
	}
	// The original GPT-5 Pro supports only high; later Pro variants support
	// medium and above. Avoid none/low for all Pro variants.
	if strings.HasPrefix(model, "gpt-5-pro") {
		return "high"
	}
	if strings.Contains(model, "pro") {
		if level == "off" || level == "low" {
			return "medium"
		}
	}
	if level == "xhigh" {
		return "high"
	}
	if level != "off" {
		return level
	}
	// Only known general-purpose GPT-5.1–5.5 families support none.
	// Codex and future/unknown reasoning families conservatively use low.
	if !strings.Contains(model, "codex") && !strings.Contains(model, "pro") {
		for _, family := range []string{"gpt-5.1", "gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5"} {
			if model == family || strings.HasPrefix(model, family+"-") {
				return "none"
			}
		}
	}
	if model == "gpt-5" || strings.HasPrefix(model, "gpt-5-2025-") || strings.HasPrefix(model, "gpt-5-mini") || strings.HasPrefix(model, "gpt-5-nano") {
		return "minimal"
	}
	return "low"
}

func setReasoningField(requestBody map[string]any, key, value string) {
	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok {
		reasoning = make(map[string]any)
		requestBody["reasoning"] = reasoning
	}
	reasoning[key] = value
}

// GetDefaultModel returns an empty string because OpenAI model IDs are
// configured by the user.
func (p *Provider) GetDefaultModel() string {
	return ""
}

// SupportsNativeSearch reports whether this provider points at the official
// OpenAI host. The factory only uses this provider for that default route.
func (p *Provider) SupportsNativeSearch() bool {
	parsed, err := url.Parse(strings.TrimSpace(p.apiBase))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

// SupportsThinking reports support for the Responses reasoning options.
func (p *Provider) SupportsThinking() bool {
	return true
}

type responseStreamEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta"`
	ItemID      string          `json:"item_id"`
	OutputIndex int             `json:"output_index"`
	Arguments   string          `json:"arguments"`
	Name        string          `json:"name"`
	Code        string          `json:"code"`
	Param       string          `json:"param"`
	Message     string          `json:"message"`
	Response    json.RawMessage `json:"response"`
	Item        json.RawMessage `json:"item"`
}

type streamToolCall struct {
	key       string
	callID    string
	name      string
	arguments strings.Builder
	emitted   bool
}

type streamOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func parseStreamResponse(ctx context.Context, body io.Reader, onChunk func(StreamChunk)) (*LLMResponse, error) {
	var textContent strings.Builder
	var reasoningContent strings.Builder
	var responseData json.RawMessage
	terminalEventType := ""
	var outputItems []json.RawMessage
	toolsByKey := make(map[string]*streamToolCall)
	toolOrder := make([]string, 0, 4)
	emittedCallIDs := make(map[string]bool)

	rememberTool := func(key string) *streamToolCall {
		if key == "" {
			key = fmt.Sprintf("output_%d", len(toolOrder))
		}
		if tool, ok := toolsByKey[key]; ok {
			return tool
		}
		tool := &streamToolCall{key: key}
		toolsByKey[key] = tool
		toolOrder = append(toolOrder, key)
		return tool
	}

	streamTool := func(tool *streamToolCall) protocoltypes.ToolCall {
		arguments := strings.TrimSpace(tool.arguments.String())
		if arguments == "" {
			arguments = "{}"
		}
		return protocoltypes.ToolCall{
			ID:        tool.callID,
			Name:      tool.name,
			Arguments: common.DecodeToolCallArguments(json.RawMessage(arguments), tool.name),
		}
	}

	emitTool := func(tool *streamToolCall) {
		if tool == nil || tool.name == "" || tool.emitted {
			return
		}
		call := streamTool(tool)
		key := call.ID
		if key == "" {
			key = tool.key
		}
		if emittedCallIDs[key] {
			tool.emitted = true
			return
		}
		tool.emitted = true
		emittedCallIDs[key] = true
		if onChunk != nil {
			onChunk(StreamChunk{ToolCalls: []protocoltypes.ToolCall{call}})
		}
	}

	processEvent := func(data string) error {
		data = strings.TrimSpace(data)
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			return io.EOF
		}

		var event responseStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("failed to decode Responses stream event: %w", err)
		}

		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			textContent.WriteString(event.Delta)
			if onChunk != nil {
				onChunk(StreamChunk{Content: textContent.String()})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoningContent.WriteString(event.Delta)
			if onChunk != nil {
				onChunk(StreamChunk{ReasoningContent: reasoningContent.String()})
			}
		case "response.output_item.added":
			item, ok := decodeStreamOutputItem(event.Item)
			if ok && item.Type == "function_call" {
				tool := rememberTool(item.ID)
				tool.callID = item.CallID
				tool.name = item.Name
				if item.Arguments != "" {
					tool.arguments.Reset()
					tool.arguments.WriteString(item.Arguments)
				}
			}
		case "response.function_call_arguments.delta":
			key := event.ItemID
			if key == "" {
				key = fmt.Sprintf("output_%d", event.OutputIndex)
			}
			tool := rememberTool(key)
			tool.arguments.WriteString(event.Delta)
		case "response.function_call_arguments.done":
			tool := rememberTool(event.ItemID)
			if event.Name != "" {
				tool.name = event.Name
			}
			if event.Arguments != "" {
				tool.arguments.Reset()
				tool.arguments.WriteString(event.Arguments)
			}
		case "response.output_item.done":
			if len(event.Item) > 0 {
				outputItems = append(outputItems, append(json.RawMessage(nil), event.Item...))
			}
			item, ok := decodeStreamOutputItem(event.Item)
			if ok && item.Type == "function_call" {
				tool := rememberTool(item.ID)
				tool.callID = item.CallID
				tool.name = item.Name
				if item.Arguments != "" {
					tool.arguments.Reset()
					tool.arguments.WriteString(item.Arguments)
				}
				emitTool(tool)
			}
		case "response.completed", "response.failed", "response.cancelled", "response.incomplete":
			if len(event.Response) > 0 && string(event.Response) != "null" {
				responseData = append(responseData[:0], event.Response...)
				terminalEventType = event.Type
			}
		case "error":
			message := strings.TrimSpace(event.Message)
			if message == "" {
				message = "unknown error"
			}
			if event.Code != "" {
				message = event.Code + ": " + message
			}
			if event.Param != "" {
				message += " (param: " + event.Param + ")"
			}
			return fmt.Errorf("Responses stream error: %s", message)
		}

		return nil
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	var eventData strings.Builder
	stopped := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			err := processEvent(eventData.String())
			eventData.Reset()
			if err == io.EOF {
				stopped = true
				break
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(data)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("Responses streaming read error: %w", err)
	}
	if !stopped && eventData.Len() > 0 {
		if err := processEvent(eventData.String()); err != nil {
			if err == io.EOF {
				stopped = true
			} else {
				return nil, err
			}
		}
	}
	if len(responseData) == 0 {
		return nil, fmt.Errorf("Responses stream ended before a terminal response event")
	}

	for _, key := range toolOrder {
		emitTool(toolsByKey[key])
	}

	if len(outputItems) > 0 {
		responseData = mergeCompletedResponseOutput(responseData, outputItems)
	}

	parsed, err := parseProviderResponse(bytes.NewReader(responseData), terminalEventType)
	if err != nil {
		return nil, err
	}
	if parsed.Content == "" && textContent.Len() > 0 {
		parsed.Content = textContent.String()
	}
	if parsed.ReasoningContent == "" && reasoningContent.Len() > 0 {
		parsed.ReasoningContent = reasoningContent.String()
	}
	if len(parsed.ToolCalls) == 0 {
		for _, key := range toolOrder {
			tool := toolsByKey[key]
			if tool == nil || tool.name == "" {
				continue
			}
			parsed.ToolCalls = append(parsed.ToolCalls, streamTool(tool))
		}
		if len(parsed.ToolCalls) > 0 {
			parsed.FinishReason = "tool_calls"
		}
	}
	return parsed, nil
}

func parseProviderResponse(body io.Reader, terminalEventType string) (*LLMResponse, error) {
	var response responses.Response
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		return nil, err
	}
	if err := rejectTerminalResponse(&response, terminalEventType); err != nil {
		return nil, err
	}
	return orc.ParseResponseFromStruct(&response), nil
}

func rejectTerminalResponse(response *responses.Response, terminalEventType string) error {
	status := response.Status
	switch terminalEventType {
	case "response.failed":
		status = responses.ResponseStatusFailed
	case "response.cancelled":
		status = responses.ResponseStatusCancelled
	}

	if status != responses.ResponseStatusFailed && status != responses.ResponseStatusCancelled {
		return nil
	}

	detail := strings.TrimSpace(string(response.Error.Code))
	message := strings.TrimSpace(response.Error.Message)
	switch {
	case detail != "" && message != "":
		detail += ": " + message
	case message != "":
		detail = message
	case detail == "":
		detail = "no error details provided"
	}

	return fmt.Errorf("OpenAI Responses API returned %s response: %s", status, detail)
}

func decodeStreamOutputItem(raw json.RawMessage) (streamOutputItem, bool) {
	if len(raw) == 0 {
		return streamOutputItem{}, false
	}
	var item streamOutputItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return streamOutputItem{}, false
	}
	return item, item.Type != ""
}

func mergeCompletedResponseOutput(responseData json.RawMessage, outputItems []json.RawMessage) json.RawMessage {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(responseData, &response); err != nil {
		return responseData
	}
	if rawOutput := response["output"]; len(rawOutput) > 0 && string(rawOutput) != "null" && string(rawOutput) != "[]" {
		return responseData
	}
	output, err := json.Marshal(outputItems)
	if err != nil {
		return responseData
	}
	response["output"] = output
	merged, err := json.Marshal(response)
	if err != nil {
		return responseData
	}
	return merged
}

func withStreamingReadIdleTimeout(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if body == nil || timeout <= 0 {
		return body
	}
	return &streamingReadIdleTimeoutBody{body: body, timeout: timeout}
}

type streamingReadIdleTimeoutBody struct {
	body    io.ReadCloser
	timeout time.Duration
}

func (b *streamingReadIdleTimeoutBody) Read(p []byte) (int, error) {
	timedOut := make(chan struct{})
	timer := time.AfterFunc(b.timeout, func() {
		close(timedOut)
		_ = b.body.Close()
	})
	n, err := b.body.Read(p)
	if !timer.Stop() {
		<-timedOut
		return n, fmt.Errorf("stream idle timeout after %s", b.timeout)
	}
	return n, err
}

func (b *streamingReadIdleTimeoutBody) Close() error {
	return b.body.Close()
}
