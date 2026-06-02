//go:build whatsapp_native

package whatsapp

import (
	"database/sql"
	"strings"
)

// aiToggleStore persists per-chat AI auto-response state.
// Absent entries default to enabled.
type aiToggleStore interface {
	isEnabled(jid string) (bool, error)
	setEnabled(jid string, enabled bool) error
}

// sqlAIToggleStore stores toggle state in the shared whatsmeow store.db.
type sqlAIToggleStore struct {
	db *sql.DB
}

func newAIToggleStore(db *sql.DB) (*sqlAIToggleStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS picoclaw_ai_toggle (
		jid     TEXT    PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 1
	)`)
	if err != nil {
		return nil, err
	}
	return &sqlAIToggleStore{db: db}, nil
}

func (s *sqlAIToggleStore) isEnabled(jid string) (bool, error) {
	var enabled int
	err := s.db.QueryRow("SELECT enabled FROM picoclaw_ai_toggle WHERE jid = ?", jid).Scan(&enabled)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return enabled != 0, nil
}

func (s *sqlAIToggleStore) setEnabled(jid string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO picoclaw_ai_toggle (jid, enabled) VALUES (?, ?)`,
		jid, val,
	)
	return err
}

// memAIToggleStore is an in-memory implementation used in tests.
type memAIToggleStore struct {
	state map[string]bool // jid → enabled; missing means enabled (default)
}

func newMemAIToggleStore() *memAIToggleStore {
	return &memAIToggleStore{state: make(map[string]bool)}
}

func (s *memAIToggleStore) isEnabled(jid string) (bool, error) {
	if v, ok := s.state[jid]; ok {
		return v, nil
	}
	return true, nil
}

func (s *memAIToggleStore) setEnabled(jid string, enabled bool) error {
	s.state[jid] = enabled
	return nil
}

// parseAIToggleCommand returns (enabled, true) when content is "/ai on" or
// "/ai off" (case-insensitive, leading/trailing whitespace ignored).
func parseAIToggleCommand(content string) (enable bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "/ai on":
		return true, true
	case "/ai off":
		return false, true
	}
	return false, false
}

// cmdDedupeStore prevents duplicate processing of toggle commands on redelivery.
// Keys are (chat JID, message ID) pairs.
//
// isSeen and markSeen are intentionally separate so that a command is not
// permanently recorded as processed until after the state mutation succeeds.
// That way a redelivered command can retry after a transient persistence failure.
type cmdDedupeStore interface {
	// isSeen returns true if (jid, messageID) was already recorded.
	isSeen(jid, messageID string) (bool, error)
	// markSeen records the pair as processed. It is a no-op if already recorded.
	markSeen(jid, messageID string) error
}

// sqlCmdDedupeStore persists command dedup state in the shared store.db.
type sqlCmdDedupeStore struct {
	db *sql.DB
}

func newCmdDedupeStore(db *sql.DB) (*sqlCmdDedupeStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS picoclaw_cmd_dedupe (
		chat_jid   TEXT NOT NULL,
		message_id TEXT NOT NULL,
		PRIMARY KEY (chat_jid, message_id)
	)`)
	if err != nil {
		return nil, err
	}
	return &sqlCmdDedupeStore{db: db}, nil
}

func (s *sqlCmdDedupeStore) isSeen(jid, messageID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM picoclaw_cmd_dedupe WHERE chat_jid = ? AND message_id = ?`,
		jid, messageID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *sqlCmdDedupeStore) markSeen(jid, messageID string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO picoclaw_cmd_dedupe (chat_jid, message_id) VALUES (?, ?)`,
		jid, messageID,
	)
	return err
}

// memCmdDedupeStore is an in-memory implementation used in tests.
type memCmdDedupeStore struct {
	seen map[string]struct{}
}

func newMemCmdDedupeStore() *memCmdDedupeStore {
	return &memCmdDedupeStore{seen: make(map[string]struct{})}
}

func (s *memCmdDedupeStore) isSeen(jid, messageID string) (bool, error) {
	key := jid + "\x00" + messageID
	_, exists := s.seen[key]
	return exists, nil
}

func (s *memCmdDedupeStore) markSeen(jid, messageID string) error {
	s.seen[jid+"\x00"+messageID] = struct{}{}
	return nil
}
