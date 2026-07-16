package evolution

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/skills"
)

var ErrApplyDraftFailed = errors.New("apply draft failed")

const taskRecordAppendDrainTimeout = 5 * time.Second

type RuntimeOptions struct {
	Config              config.EvolutionConfig
	Now                 func() time.Time
	Store               *Store
	Organizer           *Organizer
	PatternClusterer    PatternClusterer
	SuccessJudge        SuccessJudge
	SkillsRecaller      *SkillsRecaller
	DraftGenerator      DraftGenerator
	GeneratorFactory    func(workspace string) DraftGenerator
	SuccessJudgeFactory func(workspace string) SuccessJudge
	Applier             *Applier
	ApplierFactory      func(workspace string) *Applier
	TaskRecordEnricher  TaskRecordEnricher
}

type Runtime struct {
	cfg                 config.EvolutionConfig
	mu                  sync.Mutex
	now                 func() time.Time
	writer              *CaseWriter
	store               *Store
	organizer           *Organizer
	patternClusterer    PatternClusterer
	successJudge        SuccessJudge
	skillsRecaller      *SkillsRecaller
	draftGenerator      DraftGenerator
	generatorFactory    func(workspace string) DraftGenerator
	successJudgeFactory func(workspace string) SuccessJudge
	applier             *Applier
	applierFactory      func(workspace string) *Applier
	taskRecordEnricher  TaskRecordEnricher
}

type TurnCaseInput struct {
	Workspace             string
	WorkspaceID           string
	TurnID                string
	SessionKey            string
	AgentID               string
	Status                string
	UserMessage           string
	FinalContent          string
	ToolKinds             []string
	ToolExecutions        []ToolExecutionRecord
	ActiveSkillNames      []string
	AttemptedSkillNames   []string
	FinalSuccessfulPath   []string
	SkillContextSnapshots []SkillContextSnapshot
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	organizer := opts.Organizer
	if organizer == nil {
		organizer = NewOrganizer(OrganizerOptions{
			MinCaseCount:   opts.Config.EffectiveMinTaskCount(),
			MinSuccessRate: opts.Config.EffectiveMinSuccessRatio(),
			Now:            now,
		})
	}

	patternClusterer := opts.PatternClusterer
	if patternClusterer == nil {
		patternClusterer = NewHeuristicPatternClusterer(opts.Config.EffectiveMinTaskCount(), now)
	}

	return &Runtime{
		cfg:                 opts.Config,
		now:                 now,
		store:               opts.Store,
		organizer:           organizer,
		patternClusterer:    patternClusterer,
		successJudge:        opts.SuccessJudge,
		skillsRecaller:      opts.SkillsRecaller,
		draftGenerator:      opts.DraftGenerator,
		generatorFactory:    opts.GeneratorFactory,
		successJudgeFactory: opts.SuccessJudgeFactory,
		applier:             opts.Applier,
		applierFactory:      opts.ApplierFactory,
		taskRecordEnricher:  opts.TaskRecordEnricher,
	}, nil
}

func (rt *Runtime) FinalizeTurn(ctx context.Context, input TurnCaseInput) error {
	if rt == nil || !rt.cfg.Enabled {
		return nil
	}
	// Canonicalize the workspace identity so equivalent spellings (leading ~
	// alias, relative/clean equivalents) resolve to one actual workspace/state
	// path for records, IDs, and downstream cold-path deduplication.
	input.Workspace = rt.canonicalWorkspace(input.Workspace)
	input.WorkspaceID = input.Workspace
	if input.Workspace == "" || shouldSkipLearningRecord(input) {
		return nil
	}

	success := input.Status == "completed"
	usedSkillNames := buildUsedSkillNames(input)
	workspaceID := input.Workspace
	createdAt := rt.now()

	record := LearningRecord{
		ID:                 buildTaskRecordID(input, createdAt),
		Kind:               RecordKindTask,
		WorkspaceID:        workspaceID,
		CreatedAt:          createdAt,
		SessionKey:         input.SessionKey,
		Summary:            buildRecordSummary(input),
		UserGoal:           input.UserMessage,
		FinalOutput:        input.FinalContent,
		Status:             RecordStatus("new"),
		TurnStatus:         input.Status,
		Success:            &success,
		AttemptedToolCalls: len(input.ToolExecutions),
		ToolKinds:          uniqueTrimmedNames(input.ToolKinds),
		ToolExecutions:     append([]ToolExecutionRecord(nil), input.ToolExecutions...),
		ActiveSkillNames:   append([]string(nil), input.ActiveSkillNames...),
		UsedSkillNames:     append([]string(nil), usedSkillNames...),
	}
	if rt.taskRecordEnricher != nil && len(input.ToolExecutions) >= rt.cfg.EffectiveMinToolCallsForLLMEnrichment() {
		if enrichment, err := rt.taskRecordEnricher.Enrich(ctx, record); err == nil {
			record.Enrichment = enrichment
		} else {
			logger.WarnCF("evolution", "Task record enrichment failed; using deterministic record", map[string]any{"error": err.Error(), "turn_id": input.TurnID})
		}
	}

	paths := NewPaths(input.Workspace, rt.cfg.StateDir)

	rt.mu.Lock()
	if rt.writer == nil || rt.writer.paths.RootDir != paths.RootDir {
		rt.writer = NewCaseWriter(paths)
	}
	writer := rt.writer
	rt.mu.Unlock()

	// Enrichment is optional and may be canceled during bridge shutdown. The
	// deterministic envelope is required, so it gets an independent bounded
	// drain context rather than inheriting that cancellation.
	appendCtx, cancelAppend := context.WithTimeout(context.Background(), taskRecordAppendDrainTimeout)
	defer cancelAppend()
	if err := writer.AppendCase(appendCtx, record); err != nil {
		return err
	}

	if err := rt.recordSkillUsage(input, success); err != nil {
		return err
	}

	logger.DebugCF("evolution", "Recorded hot path learning record", map[string]any{
		"workspace":   input.Workspace,
		"turn_id":     input.TurnID,
		"success":     success,
		"used_skills": len(record.UsedSkillNames),
	})
	return nil
}

func buildTaskRecordID(input TurnCaseInput, createdAt time.Time) string {
	base := strings.TrimSpace(input.TurnID)
	if base == "" {
		base = "turn"
	}
	base = validSkillNameOrEmpty(base)
	if base == "" {
		base = "turn"
	}
	seed := strings.Join([]string{
		input.Workspace,
		input.SessionKey,
		input.AgentID,
		input.TurnID,
		createdAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")
	sum := sha1.Sum([]byte(seed))
	return base + "-" + hex.EncodeToString(sum[:6])
}

func buildRecordSummary(input TurnCaseInput) string {
	if goal := summarizeText(input.UserMessage, 160); goal != "" {
		return goal
	}
	return fmt.Sprintf("turn %s finished with status=%s", input.TurnID, input.Status)
}

func summarizeText(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxLen <= 0 {
		return text
	}
	if utf8.RuneCountInString(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		runes := []rune(text)
		return string(runes[:maxLen])
	}
	runes := []rune(text)
	return string(runes[:maxLen-3]) + "..."
}

func buildUsedSkillNames(input TurnCaseInput) []string {
	if final := uniqueTrimmedNames(input.FinalSuccessfulPath); len(final) > 0 {
		return final
	}
	out := make([]string, 0)
	for _, exec := range input.ToolExecutions {
		if !exec.Success {
			continue
		}
		out = append(out, exec.SkillNames...)
	}
	return uniqueTrimmedNames(out)
}

func shouldSkipLearningRecord(input TurnCaseInput) bool {
	if strings.EqualFold(strings.TrimSpace(input.SessionKey), "heartbeat") {
		return true
	}
	return false
}

func uniqueTrimmedNames(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (rt *Runtime) RunColdPathOnce(ctx context.Context, workspace string) (err error) {
	if rt == nil || !rt.cfg.Enabled {
		return nil
	}
	// Canonicalize before this value becomes the WorkspaceID, state path root,
	// and factory/store selection key, so aliased spellings share one execution
	// path rather than writing skills/state to a literal relative directory.
	workspace = rt.canonicalWorkspace(workspace)
	if workspace == "" {
		return nil
	}

	mode := rt.cfg.EffectiveMode()
	runID := fmt.Sprintf("%d", rt.now().UnixNano())
	if mode == "" || mode == "observe" {
		logger.DebugCF("evolution", "Skipped cold path run", map[string]any{
			"workspace": workspace,
			"mode":      mode,
			"run_id":    runID,
		})
		return nil
	}

	logger.InfoCF("evolution", "Started cold path run", map[string]any{
		"workspace": workspace,
		"mode":      mode,
		"run_id":    runID,
	})

	store := rt.storeForWorkspace(workspace)

	// Durably record the outcome of this meaningful (draft/apply) run and perform
	// bounded ledger cleanup. The terminal ledger entry is required: a durable
	// write/sync failure is joined into the run's result rather than swallowed,
	// so a run is never reported as succeeding without its terminal record. An
	// independent bounded context ensures cancellation cannot skip the entry.
	startedAt := rt.now()
	var outcome coldPathRunOutcome
	defer func() {
		finalizeCtx, cancel := context.WithTimeout(context.Background(), runLedgerFinalizeTimeout)
		defer cancel()
		if ledgerErr := rt.finalizeColdPathRun(
			finalizeCtx, store, workspace, mode, runID, startedAt, outcome, err,
		); ledgerErr != nil {
			err = errorsJoin(err, ledgerErr)
		}
	}()

	taskRecords, err := store.LoadTaskRecords()
	if err != nil {
		return err
	}
	patternRecords, err := store.LoadPatternRecords()
	if err != nil {
		return err
	}
	prunedCount, err := rt.pruneExpiredTaskRecords(store, workspace, patternRecords)
	if err != nil {
		return err
	}
	if prunedCount > 0 {
		taskRecords, err = store.LoadTaskRecords()
		if err != nil {
			return err
		}
	}
	outcome.prunedTaskCount = prunedCount
	outcome.taskRecordCount = len(taskRecords)
	logger.DebugCF("evolution", "Loaded evolution records", map[string]any{
		"workspace":     workspace,
		"task_count":    len(taskRecords),
		"pattern_count": len(patternRecords),
		"pruned_tasks":  prunedCount,
		"run_id":        runID,
	})

	admittedCount := 0
	newRuleCount := 0
	if rt.patternClusterer != nil {
		recordsForOrganizer, evidenceRecordsForOrganizer, inputErr := rt.recordsForColdPathInputs(
			ctx,
			workspace,
			taskRecords,
		)
		if inputErr != nil {
			return inputErr
		}
		recordsForOrganizer = rt.filterRecordsByMinSuccessRatio(
			workspace,
			evidenceRecordsForOrganizer,
			recordsForOrganizer,
		)
		admittedCount = countTaskLearningRecords(recordsForOrganizer)
		outcome.admittedTaskCount = admittedCount
		logger.DebugCF("evolution", "Admitted task records for cold path", map[string]any{
			"workspace":       workspace,
			"admitted_tasks":  admittedCount,
			"organizer_input": len(recordsForOrganizer),
			"task_ids":        joinRecordIDs(recordsForOrganizer),
			"run_id":          runID,
		})
		var rules []LearningRecord
		var clusteredTaskIDs []string
		if clusterer, ok := rt.patternClusterer.(evidencePatternClusterer); ok {
			rules, clusteredTaskIDs, err = clusterer.BuildPatternsWithEvidence(
				ctx,
				workspace,
				recordsForOrganizer,
				evidenceRecordsForOrganizer,
				patternRecords,
				rt.cfg.EffectiveMinSuccessRatio(),
			)
		} else {
			rules, clusteredTaskIDs, err = rt.patternClusterer.BuildPatterns(
				ctx,
				workspace,
				recordsForOrganizer,
				patternRecords,
			)
		}
		if err != nil {
			return err
		}
		newRuleCount = countNewPatterns(patternRecords, rules, workspace)
		outcome.newPatternCount = newRuleCount
		logger.DebugCF("evolution", "Built learning patterns", map[string]any{
			"workspace":      workspace,
			"pattern_count":  len(rules),
			"new_patterns":   newRuleCount,
			"admitted_tasks": admittedCount,
			"patterns":       summarizePatternRecords(rules),
			"run_id":         runID,
		})
		if len(rules) > 0 {
			merged := mergePatternRecords(patternRecords, rules, workspace)
			if mergeErr := store.MergePatternRecords(rules); mergeErr != nil {
				return mergeErr
			}
			patternRecords = merged
		}
		if len(clusteredTaskIDs) > 0 {
			if markErr := markTaskRecordsClustered(store, clusteredTaskIDs); markErr != nil {
				return markErr
			}
		}
	}

	generator := rt.draftGeneratorForWorkspace(workspace)
	if generator == nil {
		logger.DebugCF("evolution", "Skipped drafting because no draft generator is available", map[string]any{
			"workspace": workspace,
			"run_id":    runID,
		})
		return rt.runLifecycleMaintenance(workspace, store, runID)
	}

	recaller := rt.skillsRecallerForWorkspace(workspace)
	applier := rt.applierForWorkspace(workspace)
	readyRules := filterReadyRules(patternRecords, workspace)
	readyRules = enrichReadyRulesForDrafts(readyRules, taskRecords)
	outcome.readyPatternCount = len(readyRules)
	if len(readyRules) == 0 {
		logger.DebugCF("evolution", "Finished cold path run without ready patterns", map[string]any{
			"workspace":      workspace,
			"record_count":   len(taskRecords),
			"new_patterns":   newRuleCount,
			"admitted_tasks": admittedCount,
			"run_id":         runID,
		})
		return rt.runLifecycleMaintenance(workspace, store, runID)
	}

	existingDrafts, err := store.LoadDrafts()
	if err != nil {
		return err
	}
	readyRuleByID := make(map[string]LearningRecord, len(readyRules))
	for _, rule := range readyRules {
		readyRuleByID[rule.ID] = rule
	}
	appliedExistingDrafts := 0
	changedExistingDrafts := false
	for _, draft := range existingDrafts {
		if draft.WorkspaceID != workspace || draft.Status != DraftStatusCandidate {
			continue
		}
		rule, ok := readyRuleByID[draft.SourceRecordID]
		if !ok {
			logger.DebugCF(
				"evolution",
				"Skipped existing candidate draft because its source pattern is not ready",
				map[string]any{
					"workspace":        workspace,
					"draft_id":         draft.ID,
					"source_record_id": draft.SourceRecordID,
					"run_id":           runID,
				},
			)
			continue
		}
		matches, recallErr := recaller.RecallSimilarSkills(rule)
		if recallErr != nil {
			return recallErr
		}
		draft.MatchedSkillRefs = collectSkillRefs(matches)
		var normalizationNotes []string
		evidence := draftEvidenceForRule(rule, taskRecords)
		draft, normalizationNotes = rt.normalizeDraftForWorkspace(workspace, rule, matches, evidence, draft)
		review := ReviewDraft(draft)
		draft.Status = review.Status
		draft.ReviewNotes = appendUniqueStrings(draft.ReviewNotes, append(review.ReviewNotes, normalizationNotes...)...)
		draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, review.Findings...)
		changedExistingDrafts = true
		if draft.Status != DraftStatusCandidate || mode != "apply" || applier == nil {
			if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
				return saveErr
			}
			continue
		}
		updatedDraft, applyErr := rt.applyReviewedCandidateDraft(
			ctx, workspace, store, applier, generator, rule, matches, evidence, draft, runID,
		)
		if applyErr != nil {
			return applyErr
		}
		if updatedDraft.Status == DraftStatusAccepted {
			appliedExistingDrafts++
			changedExistingDrafts = true
		}
	}
	if changedExistingDrafts {
		existingDrafts, err = store.LoadDrafts()
		if err != nil {
			return err
		}
	}
	existingBySource := existingDraftSourceSet(existingDrafts, workspace)
	logger.DebugCF("evolution", "Selected ready patterns for drafting", map[string]any{
		"workspace":            workspace,
		"ready_patterns":       len(readyRules),
		"existing_draft_count": len(existingBySource),
		"applied_existing":     appliedExistingDrafts,
		"ready_pattern_ids":    joinRecordIDs(readyRules),
		"ready_patterns_info":  summarizePatternRecords(readyRules),
		"run_id":               runID,
	})

	processedRules := 0
	for _, rule := range readyRules {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, exists := existingBySource[rule.ID]; exists {
			logger.DebugCF(
				"evolution",
				"Skipped pattern because a non-quarantined draft already exists",
				map[string]any{
					"workspace":    workspace,
					"pattern_id":   rule.ID,
					"pattern_info": summarizePatternRecord(rule),
					"run_id":       runID,
				},
			)
			continue
		}

		evidence := draftEvidenceForRule(rule, taskRecords)
		rule = enrichRuleWithDraftEvidence(rule, evidence)
		matches, err := recaller.RecallSimilarSkills(rule)
		if err != nil {
			return err
		}
		logger.DebugCF("evolution", "Generating skill draft", map[string]any{
			"workspace":           workspace,
			"pattern_id":          rule.ID,
			"matched_skill_count": len(matches),
			"pattern_info":        summarizePatternRecord(rule),
			"run_id":              runID,
		})

		draft, err := generateDraftWithEvidence(ctx, generator, rule, matches, evidence)
		if err != nil {
			return err
		}

		draft = rt.finalizeDraft(workspace, rule, matches, evidence, draft)
		draftSaved := false
		logger.DebugCF("evolution", "Finalized skill draft", map[string]any{
			"workspace":    workspace,
			"pattern_id":   rule.ID,
			"draft_id":     draft.ID,
			"target_skill": draft.TargetSkillName,
			"change_kind":  string(draft.ChangeKind),
			"status":       string(draft.Status),
			"run_id":       runID,
		})
		if mode == "apply" && applier != nil && draft.Status == DraftStatusCandidate {
			var err error
			draft, err = rt.applyReviewedCandidateDraft(
				ctx, workspace, store, applier, generator, rule, matches, evidence, draft, runID,
			)
			if err != nil {
				return err
			}
			draftSaved = true
		}

		if !draftSaved {
			if err := store.SaveDrafts([]SkillDraft{draft}); err != nil {
				return err
			}
		}
		logger.DebugCF("evolution", "Saved skill draft", map[string]any{
			"workspace":    workspace,
			"draft_id":     draft.ID,
			"target_skill": draft.TargetSkillName,
			"status":       string(draft.Status),
			"run_id":       runID,
		})
		existingBySource[rule.ID] = struct{}{}
		processedRules++
		outcome.processedPatterns = processedRules
	}

	logger.InfoCF("evolution", "Finished cold path run", map[string]any{
		"workspace":          workspace,
		"ready_patterns":     len(readyRules),
		"processed_patterns": processedRules,
		"new_patterns":       newRuleCount,
		"run_id":             runID,
	})
	return rt.runLifecycleMaintenance(workspace, store, runID)
}

func (rt *Runtime) pruneExpiredTaskRecords(
	store *Store,
	workspace string,
	patterns []LearningRecord,
) (int, error) {
	// Normal loaded configs carry the explicit default. Keep zero-value configs used by
	// embedders and focused runtimes non-destructive unless retention was configured.
	if rt.cfg.TaskRecordRetentionHours <= 0 {
		return 0, nil
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		return 0, err
	}
	profiles, err := store.LoadProfiles()
	if err != nil {
		return 0, err
	}

	livePatternIDs := make(map[string]struct{})
	for _, pattern := range patterns {
		if pattern.WorkspaceID == workspace && pattern.Status == RecordStatus("ready") {
			livePatternIDs[pattern.ID] = struct{}{}
		}
	}
	liveDraftIDs := make(map[string]struct{})
	for _, profile := range profiles {
		if !profileBelongsToWorkspace(store.paths, workspace, profile) || profile.Status == SkillStatusDeleted {
			continue
		}
		for _, version := range profile.VersionHistory {
			if version.DraftID != "" {
				liveDraftIDs[version.DraftID] = struct{}{}
			}
		}
	}
	for _, draft := range drafts {
		if draft.WorkspaceID != workspace {
			continue
		}
		_, applied := liveDraftIDs[draft.ID]
		if draft.Status == DraftStatusCandidate || applied {
			livePatternIDs[draft.SourceRecordID] = struct{}{}
		}
	}

	protectedTaskIDs := make(map[string]struct{})
	for _, pattern := range patterns {
		if pattern.WorkspaceID != workspace {
			continue
		}
		if _, live := livePatternIDs[pattern.ID]; !live {
			continue
		}
		for _, taskID := range pattern.TaskRecordIDs {
			protectedTaskIDs[taskID] = struct{}{}
		}
	}
	retention := time.Duration(rt.cfg.EffectiveTaskRecordRetentionHours()) * time.Hour
	return store.PruneTaskRecords(workspace, rt.now().Add(-retention), protectedTaskIDs)
}

func (rt *Runtime) recordsForColdPathInputs(
	ctx context.Context,
	workspace string,
	records []LearningRecord,
) ([]LearningRecord, []LearningRecord, error) {
	admitted := make([]LearningRecord, 0, len(records))
	evidence := make([]LearningRecord, 0, len(records))
	judge := rt.successJudgeForWorkspace(workspace)

	for _, record := range records {
		if !isTaskRecordKind(record.Kind) || record.WorkspaceID != workspace {
			continue
		}
		if reason := coldPathEvidenceRejectReason(record); reason != "" {
			logger.DebugCF("evolution", "Rejected task record for cold path", map[string]any{
				"workspace": workspace,
				"record_id": record.ID,
				"reason":    reason,
			})
			continue
		}

		evidenceRecord := record
		if record.Success != nil && *record.Success && judge != nil {
			decision, err := judge.JudgeTaskRecord(ctx, record)
			if err != nil {
				return nil, nil, err
			}
			judgedSuccess := decision.Success
			evidenceRecord.Success = &judgedSuccess
			if !decision.Success {
				logger.DebugCF("evolution", "Rejected task record by success judge", map[string]any{
					"workspace": workspace,
					"record_id": record.ID,
					"reason":    strings.TrimSpace(decision.Reason),
				})
			}
		}
		evidence = append(evidence, evidenceRecord)
		if evidenceRecord.Success == nil || !*evidenceRecord.Success {
			continue
		}
		admitted = append(admitted, evidenceRecord)
	}
	return admitted, evidence, nil
}

func (rt *Runtime) filterRecordsByMinSuccessRatio(
	workspace string,
	allRecords []LearningRecord,
	admittedRecords []LearningRecord,
) []LearningRecord {
	minRatio := rt.cfg.EffectiveMinSuccessRatio()
	if minRatio <= 0 {
		return admittedRecords
	}

	type successStats struct {
		success int
		total   int
	}
	statsByKey := make(map[string]successStats)
	for _, record := range allRecords {
		key, ok := coldPathSuccessRatioKey(workspace, record)
		if !ok {
			continue
		}
		stats := statsByKey[key]
		stats.total++
		if record.Success != nil && *record.Success {
			stats.success++
		}
		statsByKey[key] = stats
	}

	out := make([]LearningRecord, 0, len(admittedRecords))
	for _, record := range admittedRecords {
		if !isTaskRecordKind(record.Kind) {
			out = append(out, record)
			continue
		}
		key, ok := coldPathSuccessRatioKey(workspace, record)
		if !ok {
			continue
		}
		stats := statsByKey[key]
		if stats.total == 0 {
			continue
		}
		ratio := float64(stats.success) / float64(stats.total)
		if ratio < minRatio {
			logger.DebugCF("evolution", "Rejected task record below cold path success ratio", map[string]any{
				"workspace":         workspace,
				"record_id":         record.ID,
				"success_ratio":     ratio,
				"min_success_ratio": minRatio,
				"success_count":     stats.success,
				"total_count":       stats.total,
			})
			continue
		}
		out = append(out, record)
	}
	return out
}

func coldPathSuccessRatioKey(workspace string, record LearningRecord) (string, bool) {
	if !isTaskRecordKind(record.Kind) || record.WorkspaceID != workspace {
		return "", false
	}
	if record.Status != "" && record.Status != RecordStatus("new") {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(record.SessionKey), "heartbeat") {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(record.FinalOutput), "HEARTBEAT_OK") {
		return "", false
	}
	if strings.TrimSpace(record.Summary) == "" {
		return "", false
	}
	key := heuristicClusterKey(record)
	if key == "" {
		return "", false
	}
	return key, true
}

func coldPathEvidenceRejectReason(record LearningRecord) string {
	if !isTaskRecordKind(record.Kind) {
		return "not a task record"
	}
	if record.Success == nil {
		return "task success unknown"
	}
	if record.Status != "" && record.Status != RecordStatus("new") {
		return "task already processed"
	}
	if strings.EqualFold(strings.TrimSpace(record.SessionKey), "heartbeat") {
		return "heartbeat session"
	}
	if strings.EqualFold(strings.TrimSpace(record.FinalOutput), "HEARTBEAT_OK") {
		return "heartbeat output"
	}
	if strings.TrimSpace(record.Summary) == "" {
		return "missing summary"
	}
	if strings.TrimSpace(record.FinalOutput) == "" {
		return "missing final output"
	}
	return ""
}

// canonicalWorkspace resolves a workspace identity to its canonical form,
// falling back to the trimmed raw value only when home expansion fails so a
// lookup error cannot silently redirect state to a wrong relative path.
func (rt *Runtime) canonicalWorkspace(workspace string) string {
	canonical, err := CanonicalWorkspace(workspace)
	if err != nil {
		logger.WarnCF("evolution", "Failed to canonicalize workspace; using raw value", map[string]any{
			"workspace": workspace,
			"error":     err.Error(),
		})
		return strings.TrimSpace(workspace)
	}
	return canonical
}

func (rt *Runtime) storeForWorkspace(workspace string) *Store {
	paths := NewPaths(workspace, rt.cfg.StateDir)
	if rt.store != nil && rt.store.paths.RootDir == paths.RootDir && rt.store.paths.Workspace == paths.Workspace {
		return rt.store
	}
	return NewStore(paths)
}

func (rt *Runtime) skillsRecallerForWorkspace(workspace string) *SkillsRecaller {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.skillsRecaller == nil || rt.skillsRecaller.workspace != workspace {
		rt.skillsRecaller = NewSkillsRecaller(workspace)
	}
	return rt.skillsRecaller
}

func (rt *Runtime) draftGeneratorForWorkspace(workspace string) DraftGenerator {
	if rt.generatorFactory != nil {
		if generator := rt.generatorFactory(workspace); generator != nil {
			return generator
		}
	}
	if rt.draftGenerator != nil {
		return rt.draftGenerator
	}
	return NewDefaultDraftGenerator(workspace)
}

func (rt *Runtime) successJudgeForWorkspace(workspace string) SuccessJudge {
	if rt.successJudgeFactory != nil {
		if judge := rt.successJudgeFactory(workspace); judge != nil {
			return judge
		}
	}
	if rt.successJudge != nil {
		return rt.successJudge
	}
	return &HeuristicSuccessJudge{}
}

func (rt *Runtime) applierForWorkspace(workspace string) *Applier {
	if rt.applierFactory != nil {
		if applier := rt.applierFactory(workspace); applier != nil {
			return applier
		}
	}
	return rt.applier
}

func (rt *Runtime) finalizeDraft(
	workspace string,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
	draft SkillDraft,
) SkillDraft {
	if draft.ID == "" {
		draft.ID = "draft-" + rule.ID
	}
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = rt.now()
	}
	draft.WorkspaceID = workspace
	draft.SourceRecordID = rule.ID
	draft.MatchedSkillRefs = collectSkillRefs(matches)

	draft, normalizationNotes := rt.normalizeDraftForWorkspace(workspace, rule, matches, evidence, draft)
	review := ReviewDraft(draft)
	draft.Status = review.Status
	draft.ReviewNotes = append([]string(nil), review.ReviewNotes...)
	draft.ReviewNotes = append(draft.ReviewNotes, normalizationNotes...)
	if len(review.Findings) == 0 {
		draft.ScanFindings = nil
		return draft
	}
	draft.ScanFindings = append([]string(nil), review.Findings...)
	return draft
}

func (rt *Runtime) normalizeDraftForWorkspace(
	workspace string,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
	draft SkillDraft,
) (SkillDraft, []string) {
	target := strings.TrimSpace(draft.TargetSkillName)
	if workspace == "" || target == "" {
		return draft, nil
	}

	notes := make([]string, 0, 4)
	if combinedTarget := inferCombinedSkillName(rule); combinedTarget != "" && combinedTarget != target {
		originalTarget := target
		draft.TargetSkillName = combinedTarget
		target = combinedTarget
		notes = append(notes, fmt.Sprintf(
			"retargeted draft from %q to combined shortcut skill %q because the winning path was a stable multi-skill chain",
			originalTarget,
			combinedTarget,
		))
	}

	skillPath := filepath.Join(workspace, "skills", target, "SKILL.md")
	existingBody, err := os.ReadFile(skillPath)
	hasExisting := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return draft, notes
	}

	if combinedTarget := inferCombinedSkillName(rule); combinedTarget != "" && combinedTarget == target {
		draft.HumanSummary = buildCombinedSkillHumanSummary(target, rule, hasExisting)
		draft.PreferredEntryPath = []string{target}
		draft.AvoidPatterns = appendUniqueStrings(
			draft.AvoidPatterns,
			buildCombinedSkillAvoidPattern(target, rule),
		)
		if hasExisting {
			draft.ChangeKind = ChangeKindReplace
			draft.BodyOrPatch = NewDefaultDraftGenerator(workspace).buildReplacementBody(
				string(existingBody), rule, evidence, matches,
			)
			notes = append(notes, "normalized combined shortcut draft to replace the existing combined skill holistically")
		} else {
			draft.ChangeKind = ChangeKindCreate
			draft.BodyOrPatch = synthesizeCombinedSkillDocument(target, draft, rule, matches, evidence)
			notes = append(notes, "normalized combined shortcut draft to create a new standalone shortcut skill")
		}
		return draft, notes
	}

	if !hasExisting {
		switch draft.ChangeKind {
		case ChangeKindAppend, ChangeKindMerge, ChangeKindReplace:
			draft.ChangeKind = ChangeKindCreate
			notes = append(notes, "normalized change_kind to create because target skill did not exist")
			if !looksLikeSkillDocument(draft.BodyOrPatch) {
				draft.BodyOrPatch = synthesizeSkillDocumentFromPartialDraft(target, draft, rule, evidence)
				notes = append(notes, "synthesized full skill document because draft body was partial")
			}
		}
		return draft, notes
	}

	if draft.ChangeKind != ChangeKindReplace {
		draft.ChangeKind = ChangeKindReplace
		draft.BodyOrPatch = NewDefaultDraftGenerator(workspace).buildReplacementBody(
			string(existingBody), rule, evidence, matches,
		)
		notes = append(notes, "normalized existing-skill update to a complete replacement")
	}
	return draft, notes
}

func looksLikeSkillDocument(body string) bool {
	body = strings.TrimSpace(body)
	return strings.HasPrefix(body, "---\n") && strings.Contains(body, "\n# ")
}

func synthesizeSkillDocumentFromPartialDraft(
	target string,
	draft SkillDraft,
	rule LearningRecord,
	evidence DraftEvidence,
) string {
	description := strings.TrimSpace(draft.HumanSummary)
	if description == "" {
		description = fmt.Sprintf("Learned workflow for %s.", target)
	}

	bodyContent := strings.TrimSpace(draft.BodyOrPatch)
	if bodyContent == "" {
		bodyContent = "No learned content was generated."
	}
	if strings.HasPrefix(bodyContent, "# ") {
		return buildSkillDocument(target, description, bodyContent)
	}

	body := strings.Join([]string{
		"# " + titleCaseSkillName(target),
		"",
		"## Start Here",
		synthesizedStartHereLine(rule, target),
		"",
		"## Learned Evolution",
		bodyContent,
		"",
		"## Expected Result",
		synthesizedExpectedResultLine(evidence),
		"",
		"## Source Evidence",
		synthesizedEvidenceLine(rule, evidence),
		"",
	}, "\n")
	return buildSkillDocument(target, description, body)
}

func synthesizeCombinedSkillDocument(
	target string,
	draft SkillDraft,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
) string {
	description := strings.TrimSpace(draft.HumanSummary)
	if description == "" {
		description = buildCombinedSkillHumanSummary(target, rule, false)
	}

	body := strings.Join([]string{
		"# " + titleCaseSkillName(target),
		"",
		"## When To Use",
		synthesizedCombinedWhenToUseLine(rule, target),
		"",
		"## Procedure",
		synthesizedCombinedStartHereLine(rule, target),
		synthesizedCombinedProcedure(matches, rule),
		"",
		"## Source Skills",
		synthesizedComponentBreakdown(matches),
		"",
		"## Learned Context",
		synthesizedCombinedLearnedContent(draft.BodyOrPatch, rule),
		"",
		"## Expected Result",
		synthesizedExpectedResultLine(evidence),
		"",
		"## Source Evidence",
		synthesizedEvidenceLine(rule, evidence),
		"",
	}, "\n")
	return buildSkillDocument(target, description, body)
}

func synthesizeCombinedSkillAppendBody(
	target string,
	draft SkillDraft,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
) string {
	lines := []string{
		"## Learned Shortcut Update",
		fmt.Sprintf("- Shortcut skill: `%s`", target),
		fmt.Sprintf("- Task summary: %s", fallbackEvolutionSummary(rule)),
		fmt.Sprintf("- Wrapped path: %s", synthesizedWrappedPathLine(rule)),
		"- Guidance: prefer this shortcut directly instead of replaying the whole path when the task matches.",
		fmt.Sprintf("- Expected result: %s", synthesizedExpectedResultLine(evidence)),
		fmt.Sprintf("- Evidence: %s", synthesizedEvidenceLine(rule, evidence)),
		"",
		"### Source Skills",
		synthesizedComponentBreakdown(matches),
		"",
		synthesizedCombinedLearnedContent(draft.BodyOrPatch, rule),
		"",
	}
	return strings.Join(lines, "\n")
}

func synthesizedStartHereLine(rule LearningRecord, target string) string {
	if len(rule.WinningPath) > 0 {
		return fmt.Sprintf(
			"Start with `%s` for tasks like `%s`.",
			strings.Join(rule.WinningPath, " -> "),
			strings.TrimSpace(rule.Summary),
		)
	}
	if summary := strings.TrimSpace(rule.Summary); summary != "" {
		return fmt.Sprintf("Use `%s` when the task matches `%s`.", target, summary)
	}
	return fmt.Sprintf("Use `%s` for the learned task pattern.", target)
}

func synthesizedCombinedStartHereLine(rule LearningRecord, target string) string {
	return fmt.Sprintf("Use `%s` directly when the task matches `%s`.", target, fallbackEvolutionSummary(rule))
}

func synthesizedCombinedWhenToUseLine(rule LearningRecord, target string) string {
	if len(rule.WinningPath) == 0 {
		return fmt.Sprintf("Use `%s` when the learned task pattern appears again.", target)
	}
	return fmt.Sprintf(
		"Use `%s` as a direct shortcut instead of replaying `%s` step by step.",
		target,
		strings.Join(rule.WinningPath, " -> "),
	)
}

func synthesizedCombinedProcedure(matches []skills.SkillInfo, rule LearningRecord) string {
	components := synthesizedComponentBreakdown(matches)
	if !strings.HasPrefix(strings.TrimSpace(components), "- `") {
		if len(rule.WinningPath) == 0 {
			return "Use the learned shortcut directly and keep the response focused on the requested result."
		}
		return fmt.Sprintf(
			"Apply the recorded path `%s`, then return the final result with only the necessary explanation.",
			strings.Join(rule.WinningPath, " -> "),
		)
	}
	return "Follow the source skill guidance below as one compact procedure, then return the final result without replaying unnecessary discovery steps."
}

func synthesizedExpectedResultLine(evidence DraftEvidence) string {
	if excerpt := firstFinalOutputExcerpt(evidence, 360); excerpt != "" {
		return excerpt
	}
	return "Return the completed result for the matched task without restating unrelated discovery steps."
}

func synthesizedEvidenceLine(rule LearningRecord, evidence DraftEvidence) string {
	if len(evidence.TaskRecords) > 0 {
		ids := make([]string, 0, len(evidence.TaskRecords))
		for _, task := range evidence.TaskRecords {
			if id := strings.TrimSpace(task.ID); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return "learned from task records: " + strings.Join(ids, ", ")
		}
	}
	if len(rule.TaskRecordIDs) > 0 {
		return "learned from task records: " + strings.Join(rule.TaskRecordIDs, ", ")
	}
	return "learned from the pattern record."
}

func synthesizedWrappedPathLine(rule LearningRecord) string {
	if len(rule.WinningPath) == 0 {
		return "No explicit wrapped path was recorded."
	}
	return strings.Join(rule.WinningPath, " -> ")
}

func synthesizedCombinedLearnedContent(body string, rule LearningRecord) string {
	content := strings.TrimSpace(stripSkillFrontmatter(body))
	if content == "" {
		return fmt.Sprintf(
			"Learned from `%s`; use this shortcut directly when the same task pattern appears again.",
			fallbackEvolutionSummary(rule),
		)
	}
	content = removeVerboseCombinedSections(content)
	content = strings.Join(strings.Fields(content), " ")
	if content == "" {
		return fmt.Sprintf(
			"Learned from `%s`; use this shortcut directly when the same task pattern appears again.",
			fallbackEvolutionSummary(rule),
		)
	}
	content = trimAtReadableBoundary(content, 1200)
	return "- Learned task: " + fallbackEvolutionSummary(rule) + "\n- Reusable guidance: " + content
}

func stripSkillFrontmatter(body string) string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "---\n") {
		return trimmed
	}
	rest := strings.TrimPrefix(trimmed, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return trimmed
	}
	return strings.TrimSpace(rest[end+5:])
}

func removeVerboseCombinedSections(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			normalized := strings.ToLower(title)
			switch normalized {
			case "component skill breakdown", "source skills", "wrapped path", "start here", "when to use", "procedure":
				skip = true
				continue
			default:
				skip = false
			}
		}
		if skip {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func fallbackEvolutionSummary(rule LearningRecord) string {
	if summary := strings.TrimSpace(rule.Summary); summary != "" {
		return summary
	}
	if len(rule.WinningPath) > 0 {
		return strings.Join(rule.WinningPath, " -> ")
	}
	return "the learned task pattern"
}

func buildCombinedSkillHumanSummary(target string, rule LearningRecord, hasExisting bool) string {
	_ = hasExisting
	summary := fallbackEvolutionSummary(rule)
	if strings.TrimSpace(summary) == "" || summary == "the learned task pattern" {
		summary = titleCaseSkillName(target)
	}
	return fmt.Sprintf("Use this skill to %s when the task requires this workflow.", sentenceFragment(summary))
}

func buildCombinedSkillAvoidPattern(target string, rule LearningRecord) string {
	if len(rule.WinningPath) == 0 {
		return fmt.Sprintf("avoid bypassing `%s` when the same learned task pattern appears again", target)
	}
	return fmt.Sprintf("avoid replaying %s before trying `%s` directly", strings.Join(rule.WinningPath, " -> "), target)
}

func collectSkillRefs(matches []skills.SkillInfo) []string {
	if len(matches) == 0 {
		return nil
	}

	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		if strings := match.Path; strings != "" {
			refs = append(refs, strings)
			continue
		}
		refs = append(refs, match.Source+":"+match.Name)
	}
	return refs
}

func countTaskLearningRecords(records []LearningRecord) int {
	count := 0
	for _, record := range records {
		if isTaskRecordKind(record.Kind) {
			count++
		}
	}
	return count
}

func (rt *Runtime) runLifecycleMaintenance(workspace string, store *Store, runID string) error {
	if rt == nil || store == nil || workspace == "" {
		return nil
	}

	paths := NewPaths(workspace, rt.cfg.StateDir)
	logger.DebugCF("evolution", "Started lifecycle maintenance", map[string]any{
		"workspace": workspace,
		"run_id":    runID,
	})

	summary, err := RunLifecycleOnce(store, paths, workspace, rt.now())
	if err != nil {
		logger.WarnCF("evolution", "Lifecycle maintenance failed", map[string]any{
			"workspace": workspace,
			"run_id":    runID,
			"error":     err.Error(),
		})
		return err
	}

	logger.DebugCF("evolution", "Finished lifecycle maintenance", map[string]any{
		"workspace":             workspace,
		"run_id":                runID,
		"evaluated_profiles":    summary.EvaluatedProfiles,
		"transitioned_profiles": summary.TransitionedProfiles,
		"deleted_skills":        summary.DeletedSkills,
	})
	return nil
}

func joinRecordIDs(records []LearningRecord) string {
	if len(records) == 0 {
		return ""
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			continue
		}
		ids = append(ids, record.ID)
	}
	return strings.Join(ids, ",")
}

func summarizePatternRecords(records []LearningRecord) string {
	if len(records) == 0 {
		return ""
	}
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, summarizePatternRecord(record))
	}
	return strings.Join(parts, " | ")
}

func summarizePatternRecord(record LearningRecord) string {
	label := strings.TrimSpace(record.ID)
	if label == "" {
		label = "unknown-pattern"
	}

	path := strings.Join(record.WinningPath, " -> ")
	if path == "" {
		path = strings.TrimSpace(record.Summary)
	}
	if path == "" {
		path = "no-summary"
	}

	return fmt.Sprintf("%s[%s]", label, path)
}

func enrichReadyRulesForDrafts(rules, taskRecords []LearningRecord) []LearningRecord {
	if len(rules) == 0 || len(taskRecords) == 0 {
		return rules
	}
	out := make([]LearningRecord, 0, len(rules))
	for _, rule := range rules {
		evidence := draftEvidenceForRule(rule, taskRecords)
		out = append(out, enrichRuleWithDraftEvidence(rule, evidence))
	}
	return out
}

func draftEvidenceForRule(rule LearningRecord, taskRecords []LearningRecord) DraftEvidence {
	if len(rule.TaskRecordIDs) == 0 || len(taskRecords) == 0 {
		return DraftEvidence{}
	}
	idSet := make(map[string]struct{}, len(rule.TaskRecordIDs))
	for _, id := range rule.TaskRecordIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		idSet[id] = struct{}{}
	}
	if len(idSet) == 0 {
		return DraftEvidence{}
	}
	tasks := make([]LearningRecord, 0, len(idSet))
	for _, task := range taskRecords {
		if rule.WorkspaceID != "" && task.WorkspaceID != rule.WorkspaceID {
			continue
		}
		if _, ok := idSet[task.ID]; !ok {
			continue
		}
		tasks = append(tasks, task)
	}
	return DraftEvidence{TaskRecords: tasks}
}

func generateDraftWithEvidence(
	ctx context.Context,
	generator DraftGenerator,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
) (SkillDraft, error) {
	if generator == nil {
		return SkillDraft{}, nil
	}
	if evidenceAware, ok := generator.(EvidenceAwareDraftGenerator); ok {
		return evidenceAware.GenerateDraftWithEvidence(ctx, rule, matches, evidence)
	}
	return generator.GenerateDraft(ctx, rule, matches)
}

func countNewPatterns(existing, patterns []LearningRecord, workspace string) int {
	existingIDs := make(map[string]struct{}, len(existing))
	for _, pattern := range existing {
		if !isPatternRecordKind(pattern.Kind) || pattern.WorkspaceID != workspace {
			continue
		}
		existingIDs[pattern.ID] = struct{}{}
	}
	count := 0
	for _, pattern := range patterns {
		if pattern.WorkspaceID != workspace {
			continue
		}
		if _, ok := existingIDs[pattern.ID]; ok {
			continue
		}
		count++
	}
	return count
}

func mergePatternRecords(existing, updates []LearningRecord, workspace string) []LearningRecord {
	out := append([]LearningRecord(nil), existing...)
	indexByID := make(map[string]int, len(out))
	for i, pattern := range out {
		indexByID[pattern.ID] = i
	}
	for _, update := range updates {
		if update.WorkspaceID != workspace {
			continue
		}
		if idx, ok := indexByID[update.ID]; ok {
			out[idx] = update
			continue
		}
		indexByID[update.ID] = len(out)
		out = append(out, update)
	}
	return out
}

func markTaskRecordsClustered(store *Store, ids []string) error {
	if store == nil || len(ids) == 0 {
		return nil
	}
	return store.MarkTaskRecordsClustered(ids)
}

func filterReadyRules(records []LearningRecord, workspace string) []LearningRecord {
	seen := make(map[string]LearningRecord)
	for _, record := range records {
		if !isPatternRecordKind(record.Kind) || record.WorkspaceID != workspace ||
			record.Status != RecordStatus("ready") {
			continue
		}
		seen[record.ID] = record
	}

	out := make([]LearningRecord, 0, len(seen))
	for _, record := range seen {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func existingDraftSourceSet(drafts []SkillDraft, workspace string) map[string]struct{} {
	out := make(map[string]struct{}, len(drafts))
	for _, draft := range drafts {
		if draft.WorkspaceID != workspace || draft.SourceRecordID == "" {
			continue
		}
		if draft.Status == DraftStatusQuarantined {
			continue
		}
		out[draft.SourceRecordID] = struct{}{}
	}
	return out
}

func (rt *Runtime) saveAppliedProfile(store *Store, workspace string, draft SkillDraft) error {
	now := rt.now()

	return SaveAppliedProfile(store, workspace, draft, now)
}

func (rt *Runtime) applyCandidateDraft(
	ctx context.Context,
	workspace string,
	store *Store,
	applier *Applier,
	draft SkillDraft,
	runID string,
	expected observedTargetState,
) (SkillDraft, error) {
	logger.InfoCF("evolution", "Applying skill draft", map[string]any{
		"workspace":    workspace,
		"draft_id":     draft.ID,
		"target_skill": draft.TargetSkillName,
		"change_kind":  string(draft.ChangeKind),
		"run_id":       runID,
	})
	rollbackApply, err := applier.applyDraftWithRollback(ctx, workspace, draft, expected)
	if err != nil {
		logger.WarnCF("evolution", "Skill draft apply failed", map[string]any{
			"workspace":    workspace,
			"draft_id":     draft.ID,
			"target_skill": draft.TargetSkillName,
			"error":        err.Error(),
			"run_id":       runID,
		})
		draft.Status = DraftStatusQuarantined
		draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, fmt.Sprintf("apply failed: %v", err))
		if auditErr := rt.recordRollbackAudit(store, draft, err); auditErr != nil {
			draft.ScanFindings = appendUniqueStrings(
				draft.ScanFindings,
				fmt.Sprintf("rollback audit failed: %v", auditErr),
			)
			// Wrap the apply error with %w (not %v) so a typed apply-safety
			// rejection compounded by an audit/save persistence failure keeps its
			// DraftApplySafetyError chain and stays eligible for the one bounded
			// regeneration, while errorsJoin preserves the human-readable prefix.
			if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
				return draft, errorsJoin(fmt.Errorf("%w: %w", ErrApplyDraftFailed, err), auditErr, saveErr)
			}
			return draft, errorsJoin(fmt.Errorf("%w: %w", ErrApplyDraftFailed, err), auditErr)
		}
		if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
			return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, err), saveErr)
		}
		// Preserve the typed apply-safety classification in the chain (via %w) so
		// the cold path can decide whether exactly one regeneration is eligible,
		// while keeping the "apply draft failed: ..." message unchanged.
		return draft, fmt.Errorf("%w: %w", ErrApplyDraftFailed, err)
	}

	draft.Status = DraftStatusAccepted
	if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
		logger.WarnCF("evolution", "Skill draft save failed after apply", map[string]any{
			"workspace":    workspace,
			"draft_id":     draft.ID,
			"target_skill": draft.TargetSkillName,
			"error":        saveErr.Error(),
			"run_id":       runID,
		})
		if rollbackErr := rollbackApply(); rollbackErr != nil {
			return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, saveErr), rollbackErr)
		}
		return draft, fmt.Errorf("%w: %v", ErrApplyDraftFailed, saveErr)
	}

	if err := rt.saveAppliedProfile(store, workspace, draft); err != nil {
		logger.WarnCF("evolution", "Skill profile save failed after apply", map[string]any{
			"workspace":    workspace,
			"draft_id":     draft.ID,
			"target_skill": draft.TargetSkillName,
			"error":        err.Error(),
			"run_id":       runID,
		})
		draft.Status = DraftStatusQuarantined
		draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, fmt.Sprintf("profile save failed: %v", err))
		if rollbackErr := rollbackApply(); rollbackErr != nil {
			draft.ScanFindings = appendUniqueStrings(
				draft.ScanFindings,
				fmt.Sprintf("apply rollback failed: %v", rollbackErr),
			)
			if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
				return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, err), rollbackErr, saveErr)
			}
			return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, err), rollbackErr)
		}
		if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
			return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, err), saveErr)
		}
		return draft, fmt.Errorf("%w: %v", ErrApplyDraftFailed, err)
	}
	logger.InfoCF("evolution", "Applied skill draft successfully", map[string]any{
		"workspace":    workspace,
		"draft_id":     draft.ID,
		"target_skill": draft.TargetSkillName,
		"run_id":       runID,
	})
	return draft, nil
}

// draftLineageConstraints captures the immutable identity of a draft so a
// feedback-aware regeneration cannot silently change what is being modified.
type draftLineageConstraints struct {
	id          string
	workspaceID string
	sourceID    string
	target      string
	changeKind  ChangeKind
}

func lineageConstraintsFromDraft(workspace string, draft SkillDraft) draftLineageConstraints {
	return draftLineageConstraints{
		id:          draft.ID,
		workspaceID: workspace,
		sourceID:    draft.SourceRecordID,
		target:      strings.TrimSpace(draft.TargetSkillName),
		changeKind:  draft.ChangeKind,
	}
}

// enforceLineageConstraints restores immutable identity fields on a regenerated
// draft and reports the first constraint the regeneration violated (target skill
// name or change kind). It never rewrites target/change-kind to force a match:
// a drifted regeneration is rejected, not normalized.
func enforceLineageConstraints(draft *SkillDraft, c draftLineageConstraints) string {
	if target := strings.TrimSpace(draft.TargetSkillName); target != "" && target != c.target {
		return fmt.Sprintf("target skill changed from %q to %q", c.target, target)
	}
	if draft.ChangeKind != "" && draft.ChangeKind != c.changeKind {
		return fmt.Sprintf("change kind changed from %q to %q", c.changeKind, draft.ChangeKind)
	}
	draft.ID = c.id
	draft.WorkspaceID = c.workspaceID
	draft.SourceRecordID = c.sourceID
	draft.TargetSkillName = c.target
	draft.ChangeKind = c.changeKind
	return ""
}

// applyReviewedCandidateDraft applies a candidate draft after, for a complete
// replacement of an existing on-disk skill, running exactly ONE proactive
// criteria-based old-vs-candidate review pass before apply. The reviewer output
// is authoritative: it is applied once the deterministic schema/type, name/path,
// required-frontmatter, and secret-scan gates pass. There is no structural-diff
// heuristic, no natural-language safety-wording heuristic, and no second review
// or apply retry.
//
// The reviewer must be usable. A mandatory review that CANNOT run (no reviewer
// capability, an unconfigured provider/model, a provider error, a nil/empty or
// malformed response, or an invalid reviewed draft) fails the apply CLOSED before
// any write rather than silently applying the unreviewed candidate. A
// caller-driven cancellation/deadline is surfaced as the context error (recorded
// as canceled) with no write. Any other change kind, or a replacement whose
// target is absent on disk, applies the candidate directly through the
// deterministic gates.
func (rt *Runtime) applyReviewedCandidateDraft(
	ctx context.Context,
	workspace string,
	store *Store,
	applier *Applier,
	generator DraftGenerator,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
	draft SkillDraft,
	runID string,
) (SkillDraft, error) {
	finalDraft, expected, reviewErr := rt.reviewReplacementDraft(
		ctx, workspace, generator, rule, matches, evidence, draft, runID,
	)
	if reviewErr != nil {
		// A cancellation/deadline is propagated unchanged so the run is recorded
		// as canceled, not as a quarantined apply failure. Every other cause is a
		// mandatory-review-unavailable pre-apply failure that writes nothing.
		if errors.Is(reviewErr, context.Canceled) || errors.Is(reviewErr, context.DeadlineExceeded) {
			return draft, reviewErr
		}
		return rt.failReviewUnavailable(store, workspace, draft, reviewErr, runID)
	}
	return rt.applyCandidateDraft(ctx, workspace, store, applier, finalDraft, runID, expected)
}

// failReviewUnavailable fails an existing-skill replacement closed before apply
// when its mandatory proactive review could not run. It records the same
// rollback-audit and quarantine bookkeeping a pre-write apply rejection would,
// so an unreviewed replacement is never written to disk, and returns
// ErrApplyDraftFailed so the cold path treats it as a normal apply failure.
func (rt *Runtime) failReviewUnavailable(
	store *Store,
	workspace string,
	draft SkillDraft,
	cause error,
	runID string,
) (SkillDraft, error) {
	logger.WarnCF("evolution", "Refusing to apply unreviewed skill replacement", map[string]any{
		"workspace":    workspace,
		"draft_id":     draft.ID,
		"target_skill": draft.TargetSkillName,
		"error":        cause.Error(),
		"run_id":       runID,
	})
	draft.Status = DraftStatusQuarantined
	draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, fmt.Sprintf("apply blocked: %v", cause))
	if auditErr := rt.recordRollbackAudit(store, draft, cause); auditErr != nil {
		draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, fmt.Sprintf("rollback audit failed: %v", auditErr))
		if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
			return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, cause), auditErr, saveErr)
		}
		return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, cause), auditErr)
	}
	if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
		return draft, errorsJoin(fmt.Errorf("%w: %v", ErrApplyDraftFailed, cause), saveErr)
	}
	return draft, fmt.Errorf("%w: %v", ErrApplyDraftFailed, cause)
}

// reviewReplacementDraft runs the single proactive replacement review for a
// complete replacement of an existing on-disk skill. It returns the draft to
// apply, the exact target state the review observed (existence plus, if present,
// the exact body, plus the canonical ownership version — so apply can detect an
// intervening edit, Gap 3, AND a later evolution-owned apply that wrote
// byte-identical content, which the body comparison alone cannot catch), and an
// error whenever the MANDATORY review could not produce a usable, valid reviewed
// draft.
//
// The observed target state is explicit rather than an empty-string sentinel, so
// an absent target, an existing-but-empty target, and "no guard applies" stay
// distinct: apply can fail closed on an absent->created or an empty->changed
// race, not only on a nonempty body that changed.
//
// Under the trust-the-reviewer model the reviewer's output is authoritative once
// it passes the deterministic gates; there is no fallback to the unreviewed
// candidate. Outcomes:
//   - not a replacement, or the target name is empty → (draft, {}, nil): no guard
//     applies and no review is required; apply the candidate directly.
//   - the replacement target is absent on disk (os.IsNotExist) → (draft,
//     {guard,absent}, nil): no review is possible, but apply must still fail closed
//     if the target is created before the write.
//   - the target cannot be read for any OTHER reason → (draft, {}, err): the
//     observed state cannot be established, so the caller fails the apply CLOSED
//     before the review rather than guessing.
//   - the review cannot run or does not yield a usable draft (no reviewer
//     capability, provider/config failure, provider error, nil/empty/malformed
//     response, invalid reviewed draft, lineage drift, or a reviewed body that
//     fails the deterministic schema/secret/frontmatter gates) → (draft, {}, err):
//     the caller fails the apply CLOSED and writes nothing.
//   - cancellation/deadline → (draft, {}, ctxErr): the caller records the run as
//     canceled and writes nothing.
//   - the reviewer produced a valid, gated refinement → (refined, {guard,exists,
//     oldBody}, nil).
//
// It issues exactly one reviewer call and never loops.
func (rt *Runtime) reviewReplacementDraft(
	ctx context.Context,
	workspace string,
	generator DraftGenerator,
	rule LearningRecord,
	matches []skills.SkillInfo,
	evidence DraftEvidence,
	draft SkillDraft,
	runID string,
) (SkillDraft, observedTargetState, error) {
	if draft.ChangeKind != ChangeKindReplace {
		return draft, observedTargetState{}, nil
	}
	target := strings.TrimSpace(draft.TargetSkillName)
	if target == "" {
		return draft, observedTargetState{}, nil
	}
	skillPath := filepath.Join(workspace, "skills", target, "SKILL.md")
	// Capture the canonical ownership version BEFORE reading the body, using the
	// SAME canonical identity apply locks and stamps on. Reading the version first
	// makes the guard fail closed against a race: an evolution-owned apply that
	// lands between this read and the eventual guarded apply (including one that
	// writes byte-identical content) stamps a version strictly higher than the one
	// captured here, so apply detects the supersession even though no lock is held
	// across the review. (If the version were read after the body, a same-byte
	// write interleaved between the two reads could be missed.) The lock is not
	// held during review, so this is the tightest guarantee the current model
	// permits; the eventual apply re-reads the version under the per-target lock.
	reviewedVersion := currentApplyOwnership(canonicalTargetIdentity(skillPath))
	oldBody, err := os.ReadFile(skillPath)
	if err != nil {
		if !os.IsNotExist(err) {
			// Only os.IsNotExist may mean observed-absent. Any other read error
			// (permissions, I/O, a path that is not a regular file) means the
			// observed state cannot be established, so fail closed before review
			// rather than treat it as an absent target.
			return draft, observedTargetState{}, fmt.Errorf(
				"%w: cannot read replacement target %q: %v", ErrReplacementReviewUnavailable, target, err)
		}
		// The target is absent on disk, so there is nothing to review; apply
		// resolves the replace-nonexistent semantics. Record the observed-absent
		// guard (with the captured version) so apply fails closed if the target is
		// created — or otherwise written by a newer evolution apply — before the
		// write.
		return draft, observedTargetState{guard: true, existed: false, versionGuard: true, version: reviewedVersion}, nil
	}
	observed := observedTargetState{guard: true, existed: true, body: string(oldBody), versionGuard: true, version: reviewedVersion}
	// From here the draft is a complete replacement of an existing skill, so the
	// proactive review is MANDATORY and its output is applied as the full,
	// complete document. If either the exact old document or the rendered
	// candidate exceeds the reviewer's per-document capacity, the prompt bounder
	// would silently truncate it, so the reviewer would judge only part of a
	// document whose output is nonetheless written as a complete replacement.
	// That is not a real review of what gets applied: fail closed before any
	// provider call or write, via the existing review-unavailable path.
	candidateDocument := renderDeployableSkillBody(draft.BodyOrPatch)
	if exceedsReplacementReviewCapacity(string(oldBody)) {
		return draft, observedTargetState{}, fmt.Errorf(
			"%w: %w: old document is %d bytes (reviewer capacity %d)",
			ErrReplacementReviewUnavailable, errReplacementDocumentTooLarge,
			len(oldBody), maxReplacementReviewBodyBytes)
	}
	if exceedsReplacementReviewCapacity(candidateDocument) {
		return draft, observedTargetState{}, fmt.Errorf(
			"%w: %w: candidate document is %d bytes (reviewer capacity %d)",
			ErrReplacementReviewUnavailable, errReplacementDocumentTooLarge,
			len(candidateDocument), maxReplacementReviewBodyBytes)
	}
	// On cancellation, fail closed with the context error before calling the
	// reviewer so nothing is ever written.
	select {
	case <-ctx.Done():
		return draft, observedTargetState{}, ctx.Err()
	default:
	}

	reviewer, ok := replacementReviewerFrom(generator)
	if !ok {
		return draft, observedTargetState{}, fmt.Errorf("%w: generator does not support replacement review", ErrReplacementReviewUnavailable)
	}

	constraints := lineageConstraintsFromDraft(workspace, draft)
	reviewed, reviewErr := reviewer.ReviewReplacement(ctx, ReplacementReviewRequest{
		Rule:              rule,
		Matches:           matches,
		Evidence:          evidence,
		CandidateDraft:    draft,
		WorkspaceID:       workspace,
		TargetSkillName:   target,
		OldDocument:       string(oldBody),
		CandidateDocument: candidateDocument,
	})
	if reviewErr != nil {
		// The mandatory review did not produce a usable result. Cancellation is
		// surfaced verbatim; everything else fails closed. Never silently apply
		// the unreviewed candidate.
		return draft, observedTargetState{}, reviewErr
	}

	// Preserve immutable identity/metadata; the reviewer may only change content.
	// A drifted reviewed draft is an invalid reviewer output, so fail closed
	// rather than applying the unreviewed candidate.
	if violation := enforceLineageConstraints(&reviewed, constraints); violation != "" {
		return draft, observedTargetState{}, fmt.Errorf("%w: reviewer changed draft lineage: %s", ErrReplacementReviewUnavailable, violation)
	}
	reviewed.CreatedAt = draft.CreatedAt
	reviewed.MatchedSkillRefs = draft.MatchedSkillRefs

	// Run the deterministic draft review (schema/type + secret scan) on the
	// reviewed body. A hard-gate failure means the reviewer output is unusable, so
	// fail closed.
	review := ReviewDraft(reviewed)
	if review.Status != DraftStatusCandidate {
		return draft, observedTargetState{}, fmt.Errorf("%w: reviewer output failed deterministic gates: %s", ErrReplacementReviewUnavailable, strings.Join(review.Findings, "; "))
	}

	// Retain the deterministic required-frontmatter-field preservation gate
	// against the exact old body. The reviewer may rename, reorganize, rephrase,
	// or drop sections and safety wording freely — that judgment is trusted — but
	// it must not drop a required frontmatter field.
	if err := validateHolisticReplacement(string(oldBody), renderDeployableSkillBody(reviewed.BodyOrPatch)); err != nil {
		return draft, observedTargetState{}, fmt.Errorf("%w: reviewer output failed frontmatter preservation: %v", ErrReplacementReviewUnavailable, err)
	}
	reviewed.Status = DraftStatusCandidate
	reviewed.ScanFindings = nil
	reviewed.ReviewNotes = appendUniqueStrings(draft.ReviewNotes,
		"refined by one proactive old-vs-candidate replacement review before apply")

	logger.InfoCF("evolution", "Refined replacement draft with one proactive review", map[string]any{
		"workspace":    workspace,
		"draft_id":     reviewed.ID,
		"target_skill": target,
		"run_id":       runID,
	})
	return reviewed, observed, nil
}

func (rt *Runtime) recordRollbackAudit(store *Store, draft SkillDraft, applyErr error) error {
	now := rt.now()
	return store.UpdateProfile(
		draft.WorkspaceID,
		draft.TargetSkillName,
		func(profile *SkillProfile, exists bool) error {
			if !exists {
				return nil
			}
			profile.VersionHistory = append(profile.VersionHistory, SkillVersionEntry{
				Version:        profile.CurrentVersion,
				Action:         "rollback",
				Timestamp:      now,
				DraftID:        draft.ID,
				Summary:        fmt.Sprintf("Rolled back failed draft apply: %s", draft.HumanSummary),
				Rollback:       true,
				RollbackReason: applyErr.Error(),
			})
			return nil
		},
	)
}

func profileOrigin(origin string) string {
	if origin == "manual" {
		return origin
	}
	return "evolved"
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		existing = append(existing, value)
		seen[value] = struct{}{}
	}
	return existing
}

type skillUsageSummary struct {
	All []string
}

func buildSkillUsage(input TurnCaseInput) skillUsageSummary {
	capacity := len(input.ActiveSkillNames) + len(input.AttemptedSkillNames) + len(input.FinalSuccessfulPath)
	for _, snapshot := range input.SkillContextSnapshots {
		capacity += len(snapshot.SkillNames)
	}
	for _, exec := range input.ToolExecutions {
		capacity += len(exec.SkillNames)
	}

	all := make([]string, 0, capacity)
	all = append(all, input.ActiveSkillNames...)
	all = append(all, input.AttemptedSkillNames...)
	all = append(all, input.FinalSuccessfulPath...)
	for _, snapshot := range input.SkillContextSnapshots {
		all = append(all, snapshot.SkillNames...)
	}
	for _, exec := range input.ToolExecutions {
		all = append(all, exec.SkillNames...)
	}
	return skillUsageSummary{All: uniqueTrimmedNames(all)}
}

func (rt *Runtime) recordSkillUsage(input TurnCaseInput, success bool) error {
	usage := buildSkillUsage(input)
	if len(usage.All) == 0 {
		return nil
	}

	store := rt.storeForWorkspace(input.Workspace)
	seen := make(map[string]struct{}, len(usage.All))
	for _, skillName := range usage.All {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}
		if _, ok := seen[skillName]; ok {
			continue
		}
		seen[skillName] = struct{}{}

		if err := rt.touchSkillProfile(store, input, skillName, success); err != nil {
			return err
		}
	}
	return nil
}

func (rt *Runtime) touchSkillProfile(store *Store, input TurnCaseInput, skillName string, success bool) error {
	now := rt.now()
	return store.UpdateProfile(input.Workspace, skillName, func(profile *SkillProfile, exists bool) error {
		if !exists {
			*profile = SkillProfile{
				SkillName:      skillName,
				WorkspaceID:    input.Workspace,
				Status:         SkillStatusActive,
				Origin:         "manual",
				HumanSummary:   skillName,
				RetentionScore: 0.2,
			}
		}

		profile.SkillName = skillName
		profile.WorkspaceID = input.Workspace
		if profile.Status == SkillStatusCold || profile.Status == SkillStatusArchived || profile.Status == "" {
			profile.Status = SkillStatusActive
		}
		if profile.Origin == "" {
			profile.Origin = "manual"
		}
		if strings.TrimSpace(profile.HumanSummary) == "" {
			profile.HumanSummary = skillName
		}
		profile.LastUsedAt = now
		profile.UseCount++
		profile.RetentionScore = nextRetentionScore(profile.RetentionScore, success)
		return nil
	})
}

func nextRetentionScore(current float64, success bool) float64 {
	increment := 0.05
	if success {
		increment = 0.1
	}
	current += increment
	if current > 1 {
		return 1
	}
	return current
}
