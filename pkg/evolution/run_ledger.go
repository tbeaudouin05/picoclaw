package evolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// runLedgerFinalizeTimeout bounds the independent finalization context used to
// record a terminal ledger entry even when the run's own context was canceled.
const runLedgerFinalizeTimeout = 5 * time.Second

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

// finalizeColdPathRun performs bounded ledger cleanup and durably appends the
// terminal entry describing the run. Pruning is maintenance: its failure is
// logged but never blocks the terminal record. A durable append/sync failure is
// returned so the caller can surface it (joined with any operation error);
// success is never reported unless the terminal entry actually reached storage.
func (rt *Runtime) finalizeColdPathRun(
	ctx context.Context,
	store *Store,
	workspace, mode, runID string,
	startedAt time.Time,
	outcome coldPathRunOutcome,
	runErr error,
) error {
	if rt == nil || store == nil || workspace == "" {
		return nil
	}

	retention := time.Duration(rt.cfg.EffectiveRunLedgerRetentionHours()) * time.Hour
	prunedLedger, pruneErr := store.PruneRunLedger(workspace, rt.now().Add(-retention))
	if pruneErr != nil {
		logger.WarnCF("evolution", "Run ledger cleanup failed", map[string]any{
			"workspace": workspace,
			"run_id":    runID,
			"error":     pruneErr.Error(),
		})
	}

	status, errSummary := classifyColdPathOutcome(runErr)
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
	if err := store.AppendRunLedgerEntryDurable(ctx, entry); err != nil {
		logger.WarnCF("evolution", "Run ledger append failed", map[string]any{
			"workspace": workspace,
			"run_id":    runID,
			"error":     err.Error(),
		})
		return fmt.Errorf("run ledger append failed: %w", err)
	}
	return nil
}

func classifyColdPathOutcome(runErr error) (status, errSummary string) {
	switch {
	case runErr == nil:
		return runLedgerStatusCompleted, ""
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return runLedgerStatusCanceled, runErr.Error()
	default:
		return runLedgerStatusFailed, runErr.Error()
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

// AppendRunLedgerEntryDurable appends one bounded entry to the run ledger and
// forces it to stable storage: the record is fsync'd and, when the ledger file
// is newly created, its parent directory is fsync'd too so the new file cannot
// vanish after a crash. Unlike AppendRunLedgerEntry it treats an oversized
// encoded entry as an explicit error rather than silently dropping it, because
// a terminal run record must not be reported as written when it was not.
//
// It uses a context-aware lock so a caller can pass an independent finalization
// context and still record a terminal entry even when the run was canceled.
func (s *Store) AppendRunLedgerEntryDurable(ctx context.Context, entry ColdPathRunLedgerEntry) error {
	unlock, err := lockStoreFileContext(ctx, s.paths.RunLedger)
	if err != nil {
		return err
	}
	defer unlock()

	return s.appendRunLedgerEntryDurableLocked(entry)
}

func (s *Store) appendRunLedgerEntryDurableLocked(entry ColdPathRunLedgerEntry) (err error) {
	dir := filepath.Dir(s.paths.RunLedger)
	if mkdirErr := os.MkdirAll(dir, 0o755); mkdirErr != nil {
		return mkdirErr
	}
	line, err := json.Marshal(boundedRunLedgerEntry(entry))
	if err != nil {
		return err
	}
	if len(line)+1 > maxJSONLRecordBytes {
		return fmt.Errorf("run ledger entry for run %q exceeds %d bytes", entry.RunID, maxJSONLRecordBytes)
	}

	_, statErr := os.Stat(s.paths.RunLedger)
	newFile := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newFile {
		return statErr
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
	if _, err = f.Write(append(line, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if newFile {
		if dirFile, dirErr := os.Open(dir); dirErr == nil {
			syncErr := dirFile.Sync()
			closeErr := dirFile.Close()
			if syncErr != nil {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
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
