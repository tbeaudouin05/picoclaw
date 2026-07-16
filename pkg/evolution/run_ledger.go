package evolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// maxRunLedgerEntries hard-bounds the run ledger per WorkspaceID, regardless of
// the configured retention window, so a misconfigured or clock-skewed workspace
// cannot grow the file without limit. The cap is applied independently to each
// workspace's entries so that, under a shared StateDir, a noisy workspace can
// never evict another workspace's entries within their retention window.
// Cleanup keeps the newest entries per workspace up to this cap.
const maxRunLedgerEntries = 1000

// ColdPathRunLedgerEntry is a durable, compact record of a single meaningful
// cold-path run outcome. It captures only bounded scalar metrics so the ledger
// stays small and safe to retain across many runs.
type ColdPathRunLedgerEntry struct {
	RunID             string    `json:"run_id"`
	WorkspaceID       string    `json:"workspace_id"`
	Mode              string    `json:"mode"`
	Status            string    `json:"status"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	ErrorSummary      string    `json:"error_summary,omitempty"`
	TaskRecordCount   int       `json:"task_record_count"`
	PrunedTaskCount   int       `json:"pruned_task_count"`
	AdmittedTaskCount int       `json:"admitted_task_count"`
	NewPatternCount   int       `json:"new_pattern_count"`
	ReadyPatternCount int       `json:"ready_pattern_count"`
	ProcessedPatterns int       `json:"processed_patterns"`
	PrunedLedgerCount int       `json:"pruned_ledger_count"`
}

const (
	runLedgerStatusCompleted = "completed"
	runLedgerStatusFailed    = "failed"
	runLedgerStatusCanceled  = "canceled"
)

// coldPathRunOutcome accumulates the meaningful metrics of a single cold-path
// run so the run ledger can record whatever stage the run reached, including
// partial progress before an early return or error.
type coldPathRunOutcome struct {
	taskRecordCount   int
	prunedTaskCount   int
	admittedTaskCount int
	newPatternCount   int
	readyPatternCount int
	processedPatterns int
}

// recordColdPathRun performs bounded ledger cleanup and appends a durable entry
// describing the run. It is best-effort: cleanup or append failures are logged
// but never alter the run's own result.
func (rt *Runtime) recordColdPathRun(
	store *Store,
	workspace, mode, runID string,
	startedAt time.Time,
	outcome coldPathRunOutcome,
	runErr error,
) {
	if rt == nil || store == nil || workspace == "" {
		return
	}

	retention := time.Duration(rt.cfg.EffectiveRunLedgerRetentionHours()) * time.Hour
	prunedLedger, err := store.PruneRunLedger(workspace, rt.now().Add(-retention))
	if err != nil {
		logger.WarnCF("evolution", "Run ledger cleanup failed", map[string]any{
			"workspace": workspace,
			"run_id":    runID,
			"error":     err.Error(),
		})
	}

	status := runLedgerStatusCompleted
	errSummary := ""
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		status = runLedgerStatusCanceled
		errSummary = runErr.Error()
	default:
		status = runLedgerStatusFailed
		errSummary = runErr.Error()
	}

	entry := ColdPathRunLedgerEntry{
		RunID:             runID,
		WorkspaceID:       workspace,
		Mode:              mode,
		Status:            status,
		StartedAt:         startedAt,
		FinishedAt:        rt.now(),
		ErrorSummary:      errSummary,
		TaskRecordCount:   outcome.taskRecordCount,
		PrunedTaskCount:   outcome.prunedTaskCount,
		AdmittedTaskCount: outcome.admittedTaskCount,
		NewPatternCount:   outcome.newPatternCount,
		ReadyPatternCount: outcome.readyPatternCount,
		ProcessedPatterns: outcome.processedPatterns,
		PrunedLedgerCount: prunedLedger,
	}
	if err := store.AppendRunLedgerEntry(entry); err != nil {
		logger.WarnCF("evolution", "Run ledger append failed", map[string]any{
			"workspace": workspace,
			"run_id":    runID,
			"error":     err.Error(),
		})
	}
}

func boundedRunLedgerEntry(entry ColdPathRunLedgerEntry) ColdPathRunLedgerEntry {
	entry.RunID = summarizeText(entry.RunID, 160)
	entry.WorkspaceID = summarizeText(entry.WorkspaceID, 300)
	entry.Mode = summarizeText(entry.Mode, 40)
	entry.Status = summarizeText(entry.Status, 40)
	entry.ErrorSummary = summarizeText(entry.ErrorSummary, 600)
	return entry
}

// LoadRunLedger returns every ledger entry currently persisted for the store's
// evolution state area, oldest lines first. Malformed lines are skipped so a
// single bad record cannot disable ledger accounting.
func (s *Store) LoadRunLedger() ([]ColdPathRunLedgerEntry, error) {
	var entries []ColdPathRunLedgerEntry
	if err := decodeJSONLLines(s.paths.RunLedger, func(line []byte) error {
		var entry ColdPathRunLedgerEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil
		}
		entries = append(entries, entry)
		return nil
	}); err != nil {
		return nil, err
	}
	return entries, nil
}

// AppendRunLedgerEntry appends one bounded entry to the run ledger.
func (s *Store) AppendRunLedgerEntry(entry ColdPathRunLedgerEntry) error {
	unlock := lockStoreFile(s.paths.RunLedger)
	defer unlock()

	return s.appendRunLedgerEntryLocked(entry)
}

func (s *Store) appendRunLedgerEntryLocked(entry ColdPathRunLedgerEntry) error {
	if mkdirErr := os.MkdirAll(filepath.Dir(s.paths.RunLedger), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	line, err := json.Marshal(boundedRunLedgerEntry(entry))
	if err != nil {
		return err
	}
	if len(line)+1 > maxJSONLRecordBytes {
		return nil // A single oversized ledger entry must never block a run.
	}
	f, err := os.OpenFile(s.paths.RunLedger, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	_, err = f.Write(append(line, '\n'))
	return err
}

// PruneRunLedger performs bounded cleanup of the run ledger. Entries for the
// given workspace older than cutoff are dropped, and the surviving entries are
// capped at maxRunLedgerEntries newest-first. It returns the number removed.
func (s *Store) PruneRunLedger(workspace string, cutoff time.Time) (int, error) {
	unlock := lockStoreFile(s.paths.RunLedger)
	defer unlock()

	entries, err := s.LoadRunLedger()
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	kept := make([]ColdPathRunLedgerEntry, 0, len(entries))
	pruned := 0
	for _, entry := range entries {
		if entry.WorkspaceID == workspace && entry.StartedAt.Before(cutoff) {
			pruned++
			continue
		}
		kept = append(kept, entry)
	}

	if capped := capRunLedgerEntries(kept, maxRunLedgerEntries); capped != nil {
		pruned += len(kept) - len(capped)
		kept = capped
	}

	if pruned == 0 {
		return 0, nil
	}
	if err := s.writeRunLedgerLocked(kept); err != nil {
		return 0, err
	}
	return pruned, nil
}

// capRunLedgerEntries caps each WorkspaceID's entries to the newest max,
// preserving original ordering, or returns nil when no workspace exceeds the
// cap. Capping per workspace (rather than across the whole file) ensures a
// noisy workspace under a shared StateDir cannot evict another workspace's
// entries within their retention window.
func capRunLedgerEntries(entries []ColdPathRunLedgerEntry, max int) []ColdPathRunLedgerEntry {
	if max <= 0 {
		return nil
	}

	indicesByWorkspace := make(map[string][]int, len(entries))
	for i, entry := range entries {
		indicesByWorkspace[entry.WorkspaceID] = append(indicesByWorkspace[entry.WorkspaceID], i)
	}

	overCap := false
	for _, indices := range indicesByWorkspace {
		if len(indices) > max {
			overCap = true
			break
		}
	}
	if !overCap {
		return nil
	}

	keepIndex := make(map[int]struct{}, len(entries))
	for _, indices := range indicesByWorkspace {
		if len(indices) <= max {
			for _, idx := range indices {
				keepIndex[idx] = struct{}{}
			}
			continue
		}
		sort.SliceStable(indices, func(a, b int) bool {
			return entries[indices[a]].StartedAt.After(entries[indices[b]].StartedAt)
		})
		for _, idx := range indices[:max] {
			keepIndex[idx] = struct{}{}
		}
	}

	capped := make([]ColdPathRunLedgerEntry, 0, len(keepIndex))
	for i, entry := range entries {
		if _, ok := keepIndex[i]; ok {
			capped = append(capped, entry)
		}
	}
	return capped
}

func (s *Store) writeRunLedgerLocked(entries []ColdPathRunLedgerEntry) error {
	if mkdirErr := os.MkdirAll(filepath.Dir(s.paths.RunLedger), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	var buf bytes.Buffer
	for _, entry := range entries {
		line, err := json.Marshal(boundedRunLedgerEntry(entry))
		if err != nil {
			return err
		}
		if len(line)+1 > maxJSONLRecordBytes {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return fileutil.WriteFileAtomic(s.paths.RunLedger, buf.Bytes(), 0o644)
}
