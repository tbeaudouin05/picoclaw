package whatsapp

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// directSeenStore tracks which direct-chat JIDs have had their first message processed.
type directSeenStore interface {
	consumeInitialDirectReply(jid string) (bool, error)
	markDirectMessageSeen(jid string) error
	close() error
}

// sqlDirectSeenStore persists first-message state in a SQLite file inside storePath.
type sqlDirectSeenStore struct {
	db *sql.DB
}

func openDirectSeenStore(storePath string) (*sqlDirectSeenStore, error) {
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(storePath, "direct_seen.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS direct_seen (jid TEXT PRIMARY KEY)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqlDirectSeenStore{db: db}, nil
}

func (s *sqlDirectSeenStore) consumeInitialDirectReply(jid string) (bool, error) {
	result, err := s.db.Exec("INSERT OR IGNORE INTO direct_seen (jid) VALUES (?)", jid)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (s *sqlDirectSeenStore) markDirectMessageSeen(jid string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO direct_seen (jid) VALUES (?)", jid)
	return err
}

func (s *sqlDirectSeenStore) close() error { return s.db.Close() }

// memDirectSeenStore is an in-memory implementation used in tests.
type memDirectSeenStore struct {
	seen map[string]bool
}

func newMemDirectSeenStore() *memDirectSeenStore {
	return &memDirectSeenStore{seen: make(map[string]bool)}
}

func (s *memDirectSeenStore) consumeInitialDirectReply(jid string) (bool, error) {
	if s.seen[jid] {
		return false, nil
	}
	s.seen[jid] = true
	return true, nil
}
func (s *memDirectSeenStore) markDirectMessageSeen(jid string) error { s.seen[jid] = true; return nil }
func (s *memDirectSeenStore) close() error                           { return nil }
