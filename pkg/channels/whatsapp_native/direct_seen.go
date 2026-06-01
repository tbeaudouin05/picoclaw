//go:build whatsapp_native

package whatsapp

import (
	"database/sql"
	"strings"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
)

// directSeenStore tracks which direct-chat JIDs have had their first message processed.
// The native implementation shares the whatsmeow store.db; lifecycle is managed by the container.
type directSeenStore interface {
	consumeInitialDirectReply(jid string) (bool, error)
	markDirectMessageSeen(jid string) error
	seedFromHistorySync(sync *waHistorySync.HistorySync) (int, error)
}

// sqlDirectSeenStore adds a table to the existing whatsmeow store DB.
type sqlDirectSeenStore struct {
	db *sql.DB
}

func newDirectSeenStore(db *sql.DB) (*sqlDirectSeenStore, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS picoclaw_direct_seen (jid TEXT PRIMARY KEY)`); err != nil {
		return nil, err
	}
	return &sqlDirectSeenStore{db: db}, nil
}

func (s *sqlDirectSeenStore) consumeInitialDirectReply(jid string) (bool, error) {
	result, err := s.db.Exec("INSERT OR IGNORE INTO picoclaw_direct_seen (jid) VALUES (?)", jid)
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
	_, err := s.db.Exec("INSERT OR IGNORE INTO picoclaw_direct_seen (jid) VALUES (?)", jid)
	return err
}

func (s *sqlDirectSeenStore) seedFromHistorySync(sync *waHistorySync.HistorySync) (int, error) {
	return seedDirectSeenFromHistorySync(s.markDirectMessageSeen, sync)
}

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
func (s *memDirectSeenStore) seedFromHistorySync(sync *waHistorySync.HistorySync) (int, error) {
	return seedDirectSeenFromHistorySync(s.markDirectMessageSeen, sync)
}

func seedDirectSeenFromHistorySync(mark func(string) error, sync *waHistorySync.HistorySync) (int, error) {
	if sync == nil {
		return 0, nil
	}
	seen := make(map[string]struct{})
	for _, conv := range sync.GetConversations() {
		jid, ok := historyDirectConversationJID(conv)
		if !ok {
			continue
		}
		if _, exists := seen[jid]; exists {
			continue
		}
		if err := mark(jid); err != nil {
			return len(seen), err
		}
		seen[jid] = struct{}{}
	}
	return len(seen), nil
}

func historyDirectConversationJID(conv *waHistorySync.Conversation) (string, bool) {
	if conv == nil || len(conv.GetMessages()) == 0 {
		return "", false
	}
	ids := []string{conv.GetID(), conv.GetNewJID(), conv.GetOldJID()}
	for _, id := range ids {
		if jid, ok := normalizeHistoryDirectJID(id); ok {
			return jid, true
		}
	}
	for _, msg := range conv.GetMessages() {
		if msg == nil || msg.GetMessage() == nil || msg.GetMessage().GetKey() == nil {
			continue
		}
		if jid, ok := normalizeHistoryDirectJID(msg.GetMessage().GetKey().GetRemoteJID()); ok {
			return jid, true
		}
	}
	return "", false
}

func normalizeHistoryDirectJID(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.IsEmpty() || jid.Server == types.GroupServer {
		return "", false
	}
	return jid.String(), true
}
