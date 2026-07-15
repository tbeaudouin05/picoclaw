package evolution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/skills"
)

type Store struct {
	paths Paths
}

const maxJSONLRecordBytes = 1024 * 1024

func NewStore(paths Paths) *Store {
	return &Store{paths: paths}
}

var storeFileLocks sync.Map

// storeFileLock is a context-aware mutex. Its single token represents an
// unlocked file, which lets callers abandon lock acquisition when their
// context expires without creating a goroutine that could later take a lock.
type storeFileLock struct {
	token chan struct{}
}

func newStoreFileLock() *storeFileLock {
	lock := &storeFileLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

func (s *Store) AppendLearningRecord(ctx context.Context, record LearningRecord) error {
	switch record.Kind {
	case RecordKindPattern, legacyRecordKindRule:
		return s.AppendPatternRecords([]LearningRecord{record})
	default:
		return s.AppendTaskRecord(ctx, record)
	}
}

func (s *Store) AppendLearningRecords(records []LearningRecord) error {
	taskRecords := make([]LearningRecord, 0, len(records))
	patternRecords := make([]LearningRecord, 0, len(records))
	for _, record := range records {
		switch record.Kind {
		case RecordKindPattern, legacyRecordKindRule:
			patternRecords = append(patternRecords, record)
		default:
			taskRecords = append(taskRecords, record)
		}
	}
	if err := s.AppendTaskRecords(context.Background(), taskRecords); err != nil {
		return err
	}
	return s.AppendPatternRecords(patternRecords)
}

func (s *Store) AppendTaskRecord(ctx context.Context, record LearningRecord) error {
	return s.AppendTaskRecords(ctx, []LearningRecord{record})
}

func (s *Store) AppendTaskRecords(ctx context.Context, records []LearningRecord) error {
	return s.appendJSONLRecords(ctx, s.paths.TaskRecords, records)
}

func (s *Store) AppendPatternRecords(records []LearningRecord) error {
	return s.appendJSONLRecords(context.Background(), s.paths.PatternRecords, records)
}

func (s *Store) appendJSONLRecords(ctx context.Context, path string, records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	unlock, err := lockStoreFileContext(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()

	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if err := repairJSONLTail(f); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	for _, record := range records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		record = boundedLearningRecord(record)
		line, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return marshalErr
		}
		if len(line)+1 > maxJSONLRecordBytes {
			return fmt.Errorf("bounded learning record exceeds JSONL line limit")
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// repairJSONLTail inspects at most one maximum-sized record. A valid final
// record merely missing its terminator is preserved; an incomplete or
// oversized tail is discarded back to the last known record boundary.
func repairJSONLTail(f *os.File) error {
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	last := []byte{0}
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	window := int64(maxJSONLRecordBytes + 1)
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	tail := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(tail, start); err != nil {
		return err
	}
	boundary := bytes.LastIndexByte(tail, '\n')
	recordStart := boundary + 1
	if boundary < 0 && start > 0 {
		priorBoundary, err := findLastNewlineBefore(f, start)
		if err != nil {
			return err
		}
		return f.Truncate(priorBoundary + 1)
	}
	fragment := bytes.TrimSpace(tail[recordStart:])
	if len(fragment) <= maxJSONLRecordBytes && json.Valid(fragment) {
		if _, err := f.WriteAt([]byte{'\n'}, info.Size()); err != nil {
			return err
		}
		return nil
	}
	return f.Truncate(start + int64(recordStart))
}

func findLastNewlineBefore(f *os.File, end int64) (int64, error) {
	const chunkSize = 64 * 1024
	buf := make([]byte, chunkSize)
	for end > 0 {
		start := end - int64(len(buf))
		if start < 0 {
			start = 0
		}
		chunk := buf[:end-start]
		if _, err := f.ReadAt(chunk, start); err != nil {
			return 0, err
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			return start + int64(index), nil
		}
		end = start
	}
	return -1, nil
}

func (s *Store) LoadLearningRecords() ([]LearningRecord, error) {
	taskRecords, err := s.LoadTaskRecords()
	if err != nil {
		return nil, err
	}
	patternRecords, err := s.LoadPatternRecords()
	if err != nil {
		return nil, err
	}
	return append(taskRecords, patternRecords...), nil
}

func (s *Store) LoadTaskRecords() ([]LearningRecord, error) {
	records, err := s.loadRecordsFromPath(s.paths.TaskRecords)
	if err != nil {
		return nil, err
	}
	legacy, err := s.loadLegacyTaskRecords()
	if err != nil {
		return nil, err
	}
	return mergeLearningRecordsByID(legacy, records), nil
}

func (s *Store) LoadPatternRecords() ([]LearningRecord, error) {
	records, err := s.loadRecordsFromPath(s.paths.PatternRecords)
	if err != nil {
		return nil, err
	}
	legacy, err := s.loadLegacyPatternRecords()
	if err != nil {
		return nil, err
	}
	return mergeLearningRecordsByID(legacy, records), nil
}

func (s *Store) loadRecordsFromPath(path string) ([]LearningRecord, error) {
	var records []LearningRecord
	if err := decodeJSONLLines(path, func(line []byte) error {
		var record LearningRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil // A bad evidence record must not disable the cold path.
		}
		records = append(records, boundedLearningRecord(record))
		return nil
	}); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) loadLegacyTaskRecords() ([]LearningRecord, error) {
	records, err := s.loadRecordsFromPath(s.paths.LearningRecords)
	if err != nil {
		return nil, err
	}
	out := make([]LearningRecord, 0, len(records))
	for _, record := range records {
		if isTaskRecordKind(record.Kind) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *Store) loadLegacyPatternRecords() ([]LearningRecord, error) {
	records, err := s.loadRecordsFromPath(s.paths.LearningRecords)
	if err != nil {
		return nil, err
	}
	out := make([]LearningRecord, 0, len(records))
	for _, record := range records {
		if isPatternRecordKind(record.Kind) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *Store) SaveTaskRecords(records []LearningRecord) error {
	return s.saveJSONLRecords(s.paths.TaskRecords, records)
}

func (s *Store) PruneTaskRecords(
	workspace string,
	cutoff time.Time,
	protectedIDs map[string]struct{},
) (int, error) {
	unlock := lockStoreFile(s.paths.TaskRecords)
	defer unlock()

	current, err := s.loadRecordsFromPath(s.paths.TaskRecords)
	if err != nil {
		return 0, err
	}
	legacy, err := s.loadLegacyTaskRecords()
	if err != nil {
		return 0, err
	}
	records := mergeLearningRecordsByID(legacy, current)
	kept := make([]LearningRecord, 0, len(records))
	pruned := 0
	for _, record := range records {
		_, protected := protectedIDs[record.ID]
		if record.WorkspaceID == workspace && !protected && record.CreatedAt.Before(cutoff) {
			pruned++
			continue
		}
		kept = append(kept, record)
	}
	if pruned == 0 {
		return 0, nil
	}

	// Migrate retained legacy tasks into the current task file, then remove only task
	// entries from the legacy mixed record file so they cannot be reloaded later.
	legacyUnlock := lockStoreFile(s.paths.LearningRecords)
	defer legacyUnlock()
	legacyRecords, err := s.loadRecordsFromPath(s.paths.LearningRecords)
	if err != nil {
		return 0, err
	}
	legacyPatterns := make([]LearningRecord, 0, len(legacyRecords))
	for _, record := range legacyRecords {
		if !isTaskRecordKind(record.Kind) {
			legacyPatterns = append(legacyPatterns, record)
		}
	}
	if err := s.saveJSONLRecordsLocked(s.paths.TaskRecords, kept); err != nil {
		return 0, err
	}
	if len(legacyRecords) > 0 {
		if err := s.saveJSONLRecordsLocked(s.paths.LearningRecords, legacyPatterns); err != nil {
			return 0, err
		}
	}
	return pruned, nil
}

func (s *Store) MarkTaskRecordsClustered(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	target := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		target[id] = struct{}{}
	}
	if len(target) == 0 {
		return nil
	}

	unlock := lockStoreFile(s.paths.TaskRecords)
	defer unlock()

	current, err := s.loadRecordsFromPath(s.paths.TaskRecords)
	if err != nil {
		return err
	}
	legacy, err := s.loadLegacyTaskRecords()
	if err != nil {
		return err
	}
	records := mergeLearningRecordsByID(legacy, current)

	hasTargetRecordInWorkspace := make(map[string]bool, len(target))
	if strings.TrimSpace(s.paths.Workspace) != "" {
		for _, record := range records {
			if _, ok := target[record.ID]; !ok {
				continue
			}
			if record.WorkspaceID == s.paths.Workspace {
				hasTargetRecordInWorkspace[record.ID] = true
			}
		}
	}

	changed := false
	for i := range records {
		if _, ok := target[records[i].ID]; !ok {
			continue
		}
		if hasTargetRecordInWorkspace[records[i].ID] && records[i].WorkspaceID != s.paths.Workspace {
			continue
		}
		records[i].Status = RecordStatus("clustered")
		changed = true
	}
	if !changed {
		return nil
	}
	return s.saveJSONLRecordsLocked(s.paths.TaskRecords, records)
}

func (s *Store) SavePatternRecords(records []LearningRecord) error {
	return s.saveJSONLRecords(s.paths.PatternRecords, records)
}

func (s *Store) MergePatternRecords(records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}

	unlock := lockStoreFile(s.paths.PatternRecords)
	defer unlock()

	current, err := s.loadRecordsFromPath(s.paths.PatternRecords)
	if err != nil {
		return err
	}
	legacy, err := s.loadLegacyPatternRecords()
	if err != nil {
		return err
	}
	merged := mergeLearningRecordsByID(mergeLearningRecordsByID(legacy, current), records)
	return s.saveJSONLRecordsLocked(s.paths.PatternRecords, merged)
}

func (s *Store) saveJSONLRecords(path string, records []LearningRecord) error {
	unlock := lockStoreFile(path)
	defer unlock()

	return s.saveJSONLRecordsLocked(path, records)
}

func (s *Store) saveJSONLRecordsLocked(path string, records []LearningRecord) error {
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	var buf bytes.Buffer
	for _, record := range records {
		record = boundedLearningRecord(record)
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(line)+1 > maxJSONLRecordBytes {
			return fmt.Errorf("bounded learning record exceeds JSONL line limit")
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return fileutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

func mergeLearningRecordsByID(base, updates []LearningRecord) []LearningRecord {
	out := append([]LearningRecord(nil), base...)
	indexByID := make(map[string]int, len(out)+len(updates))
	for i, record := range out {
		key := learningRecordMergeKey(record)
		if key == "" {
			continue
		}
		indexByID[key] = i
	}
	for _, record := range updates {
		key := learningRecordMergeKey(record)
		if key == "" {
			out = append(out, record)
			continue
		}
		if idx, ok := indexByID[key]; ok {
			out[idx] = record
			continue
		}
		indexByID[key] = len(out)
		out = append(out, record)
	}
	return out
}

func learningRecordMergeKey(record LearningRecord) string {
	id := strings.TrimSpace(record.ID)
	if id == "" {
		return ""
	}
	return strings.TrimSpace(record.WorkspaceID) + "\x00" + id
}

func (s *Store) SaveDrafts(drafts []SkillDraft) error {
	unlock := lockStoreFile(s.paths.SkillDrafts)
	defer unlock()

	existing, err := s.LoadDrafts()
	if err != nil {
		return err
	}

	indexByKey := make(map[string]int, len(existing))
	for i, draft := range existing {
		indexByKey[draftKey(draft.WorkspaceID, draft.ID)] = i
	}

	for _, draft := range drafts {
		key := draftKey(draft.WorkspaceID, draft.ID)
		if idx, ok := indexByKey[key]; ok {
			existing[idx] = draft
			continue
		}
		indexByKey[key] = len(existing)
		existing = append(existing, draft)
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(s.paths.SkillDrafts, data, 0o644)
}

func (s *Store) LoadDrafts() ([]SkillDraft, error) {
	data, err := os.ReadFile(s.paths.SkillDrafts)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	var drafts []SkillDraft
	if err := json.Unmarshal(data, &drafts); err != nil {
		return nil, err
	}
	return drafts, nil
}

func (s *Store) SaveProfile(profile SkillProfile) error {
	path, err := s.profilePath(profile.WorkspaceID, profile.SkillName)
	if err != nil {
		return err
	}
	unlock := lockStoreFile(path)
	defer unlock()

	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o644)
}

func (s *Store) LoadProfile(skillName string) (SkillProfile, error) {
	return s.loadProfileForWorkspace(strings.TrimSpace(s.paths.Workspace), skillName)
}

func (s *Store) UpdateProfile(
	workspaceID, skillName string,
	update func(profile *SkillProfile, exists bool) error,
) error {
	targetPath, err := s.profilePath(workspaceID, skillName)
	if err != nil {
		return err
	}

	unlock := lockStoreFile(targetPath)
	defer unlock()

	profile, err := s.loadProfileForWorkspace(workspaceID, skillName)
	exists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		profile = SkillProfile{}
	} else if err != nil {
		return err
	}

	if updateErr := update(&profile, exists); updateErr != nil {
		return updateErr
	}
	if !exists && isZeroSkillProfile(profile) {
		return nil
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(targetPath), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(targetPath, data, 0o644)
}

func (s *Store) loadProfileForWorkspace(workspaceID, skillName string) (SkillProfile, error) {
	paths, err := s.profileLookupPaths(workspaceID, skillName)
	if err != nil {
		return SkillProfile{}, err
	}
	for _, path := range paths {
		profile, loadErr := s.loadProfileFromPath(path)
		if errors.Is(loadErr, os.ErrNotExist) {
			continue
		}
		if loadErr != nil {
			return SkillProfile{}, loadErr
		}
		return profile, nil
	}
	return SkillProfile{}, os.ErrNotExist
}

func isZeroSkillProfile(profile SkillProfile) bool {
	return profile.SkillName == "" &&
		profile.WorkspaceID == "" &&
		profile.CurrentVersion == "" &&
		profile.Status == "" &&
		profile.Origin == "" &&
		profile.HumanSummary == "" &&
		profile.ChangeReason == "" &&
		len(profile.IntendedUseCases) == 0 &&
		len(profile.PreferredEntryPath) == 0 &&
		len(profile.AvoidPatterns) == 0 &&
		profile.LastUsedAt.IsZero() &&
		profile.UseCount == 0 &&
		profile.RetentionScore == 0 &&
		len(profile.VersionHistory) == 0
}

func (s *Store) LoadProfiles() ([]SkillProfile, error) {
	entries, err := os.ReadDir(s.paths.ProfilesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	profiles := make([]SkillProfile, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(s.paths.ProfilesDir, entry.Name())
		if entry.IsDir() {
			nestedProfiles, loadErr := s.loadProfilesFromDir(entryPath)
			if loadErr != nil {
				return nil, loadErr
			}
			profiles = append(profiles, nestedProfiles...)
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile, err := s.loadProfileFromPath(entryPath)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}

	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].SkillName != profiles[j].SkillName {
			return profiles[i].SkillName < profiles[j].SkillName
		}
		return profiles[i].WorkspaceID < profiles[j].WorkspaceID
	})
	return profiles, nil
}

func decodeJSONLLines(path string, decode func(line []byte) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, maxJSONLRecordBytes+1)
	for {
		line, readErr := reader.ReadSlice('\n')
		oversize := false
		for errors.Is(readErr, bufio.ErrBufferFull) {
			oversize = true
			_, readErr = reader.ReadSlice('\n')
		}
		if !oversize {
			line = bytes.TrimSpace(line)
			if len(line) > 0 && len(line) <= maxJSONLRecordBytes {
				if err := decode(line); err != nil && !isInvalidJSON(err) {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func draftKey(workspaceID, id string) string {
	return workspaceID + "\x00" + id
}

func isInvalidJSON(err error) bool {
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr)
}

func lockStoreFile(path string) func() {
	unlock, err := lockStoreFileContext(context.Background(), path)
	if err != nil {
		panic(err)
	}
	return unlock
}

func lockStoreFileContext(ctx context.Context, path string) (func(), error) {
	for {
		actual, _ := storeFileLocks.LoadOrStore(path, newStoreFileLock())
		lock, ok := actual.(*storeFileLock)
		if !ok || lock == nil {
			// Corrupted entry (wrong type or nil *storeFileLock).
			// Atomically swap in a fresh lock via CompareAndSwap.
			// If CAS fails, another goroutine already replaced it —
			// just retry the loop to pick up the valid entry.
			storeFileLocks.CompareAndSwap(path, actual, newStoreFileLock())
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-lock.token:
			// If the context and the token became ready together, prefer the
			// context result and put the token back before returning.
			select {
			case <-ctx.Done():
				lock.token <- struct{}{}
				return nil, ctx.Err()
			default:
			}
			return func() { lock.token <- struct{}{} }, nil
		}
	}
}

func (s *Store) profilePath(workspaceID, skillName string) (string, error) {
	if err := skills.ValidateSkillName(skillName); err != nil {
		return "", err
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return filepath.Join(s.paths.ProfilesDir, skillName+".json"), nil
	}
	return filepath.Join(s.paths.ProfilesDir, workspaceScopeDir(workspaceID), skillName+".json"), nil
}

func (s *Store) loadProfilesFromDir(dir string) ([]SkillProfile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	profiles := make([]SkillProfile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile, err := s.loadProfileFromPath(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (s *Store) loadProfileFromPath(path string) (SkillProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillProfile{}, err
	}

	var profile SkillProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return SkillProfile{}, err
	}
	return profile, nil
}

func (s *Store) profileLookupPaths(workspaceID, skillName string) ([]string, error) {
	if err := skills.ValidateSkillName(skillName); err != nil {
		return nil, err
	}

	paths := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	appendPath := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		paths = append(paths, path)
		seen[path] = struct{}{}
	}

	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID != "" {
		path, err := s.profilePath(workspaceID, skillName)
		if err != nil {
			return nil, err
		}
		appendPath(path)
		if !usesDefaultWorkspaceState(s.paths, workspaceID) {
			return paths, nil
		}
	}

	legacyPath, err := s.profilePath("", skillName)
	if err != nil {
		return nil, err
	}
	appendPath(legacyPath)
	return paths, nil
}

func workspaceScopeDir(workspaceID string) string {
	sum := sha1.Sum([]byte(workspaceID))
	base := filepath.Base(filepath.Clean(workspaceID))
	base = sanitizeWorkspaceComponent(base)
	if base == "" || base == "." {
		base = "workspace"
	}
	return base + "-" + hex.EncodeToString(sum[:6])
}

func sanitizeWorkspaceComponent(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
