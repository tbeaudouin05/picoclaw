package evolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

type TaskRecordEnricher interface {
	Enrich(context.Context, LearningRecord) (*TaskRecordEnrichment, error)
}

type LLMTaskRecordEnricher struct {
	provider providers.LLMProvider
	model    string
	timeout  time.Duration
	enabled  bool
}

const (
	taskRecordEnrichmentUserGoalLimit    = 800
	taskRecordEnrichmentFinalOutputLimit = 1200
)

func NewLLMTaskRecordEnricher(provider providers.LLMProvider, model string, timeout time.Duration, enabled bool) TaskRecordEnricher {
	model = strings.TrimSpace(model)
	if !enabled || provider == nil || model == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &LLMTaskRecordEnricher{provider: provider, model: model, timeout: timeout, enabled: true}
}

func (e *LLMTaskRecordEnricher) Enrich(ctx context.Context, record LearningRecord) (*TaskRecordEnrichment, error) {
	if e == nil || !e.enabled || e.provider == nil {
		return nil, nil
	}
	callCtx, cancel := withLLMCallTimeout(ctx, e.timeout)
	defer cancel()
	facts, err := json.Marshal(struct {
		UserMessage        string                `json:"user_message"`
		FinalOutput        string                `json:"final_output"`
		Status             RecordStatus          `json:"status"`
		TurnStatus         string                `json:"turn_status"`
		Success            *bool                 `json:"success"`
		AttemptedToolCalls int                   `json:"attempted_tool_calls"`
		ToolKinds          []string              `json:"tool_kinds"`
		ToolExecutions     []ToolExecutionRecord `json:"tool_executions"`
		UsedSkillNames     []string              `json:"used_skill_names"`
	}{
		UserMessage:        summarizeText(record.UserGoal, taskRecordEnrichmentUserGoalLimit),
		FinalOutput:        summarizeText(record.FinalOutput, taskRecordEnrichmentFinalOutputLimit),
		Status:             record.Status,
		TurnStatus:         record.TurnStatus,
		Success:            record.Success,
		AttemptedToolCalls: record.AttemptedToolCalls,
		ToolKinds:          record.ToolKinds,
		ToolExecutions:     record.ToolExecutions,
		UsedSkillNames:     record.UsedSkillNames,
	})
	if err != nil {
		return nil, err
	}
	type result struct {
		response *providers.LLMResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		resp, err := e.provider.Chat(callCtx, []providers.Message{
			{Role: "system", Content: enrichmentSystemPrompt},
			{Role: "user", Content: "Runtime-derived evidence (do not contradict or re-infer these facts):\n" + string(facts)},
		}, nil, e.model, map[string]any{"temperature": 0.1})
		resultCh <- result{response: resp, err: err}
	}()
	var resp *providers.LLMResponse
	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		resp = result.response
	case <-callCtx.Done():
		return nil, callCtx.Err()
	}
	if resp == nil {
		return nil, errors.New("empty enrichment response")
	}
	return parseTaskRecordEnrichment(resp.Content)
}

const enrichmentSystemPrompt = `Return exactly one JSON object, without markdown, with this schema:
{"summary":"string","task_type":"string","outcome_or_blocker":"string","top_frictions_errors":[{"text":"string","confidence":0.0,"evidence":"string"}],"process_improvements":[{"text":"string","confidence":0.0,"evidence":"string"}],"reusable_knowledge":[{"text":"string","confidence":0.0,"evidence":"string"}],"learning_value":{"text":"string","confidence":0.0,"evidence":"string"}}
Use only the supplied evidence. Be concise. Use empty arrays when unsupported. Each array may contain at most 3 items. Evidence must point to a supplied fact or excerpt. Suggested improvements must be scoped process changes, never broad product claims. Do not infer tool count, tools, skills, status, or success.`

func parseTaskRecordEnrichment(content string) (*TaskRecordEnrichment, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(content)))
	decoder.DisallowUnknownFields()
	var out TaskRecordEnrichment
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode enrichment: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateTaskRecordEnrichment(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("enrichment contains trailing JSON")
		}
		return fmt.Errorf("decode enrichment trailing content: %w", err)
	}
	return nil
}

func validateTaskRecordEnrichment(value *TaskRecordEnrichment) error {
	if value == nil || !boundedRequired(value.Summary, 300) || !boundedRequired(value.TaskType, 80) || !boundedRequired(value.OutcomeOrBlocker, 300) {
		return errors.New("enrichment has missing or oversized required fields")
	}
	groups := [][]EvidenceAssessment{value.TopFrictionsErrors, value.ProcessImprovements, value.ReusableKnowledge, {value.LearningValue}}
	for _, group := range groups {
		if len(group) > 3 {
			return errors.New("enrichment array exceeds 3 items")
		}
		for _, item := range group {
			if !boundedRequired(item.Text, 300) || !boundedRequired(item.Evidence, 400) || item.Confidence < 0 || item.Confidence > 1 {
				return errors.New("enrichment assessment is missing, oversized, or has invalid confidence")
			}
		}
	}
	return nil
}

func boundedRequired(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= max
}
