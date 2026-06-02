//go:build whatsapp_native

// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	sqliteDriver   = "sqlite"
	whatsappDBName = "store.db"

	reconnectInitial    = 5 * time.Second
	reconnectMax        = 5 * time.Minute
	reconnectMultiplier = 2.0
)

// WhatsAppNativeChannel implements the WhatsApp channel using whatsmeow (in-process, no external bridge).
type WhatsAppNativeChannel struct {
	*channels.BaseChannel
	config       *config.WhatsAppSettings
	storePath    string
	client       *whatsmeow.Client
	container    *sqlstore.Container
	directSeen   directSeenStore // non-nil when AllowInitialDirectReply is enabled
	aiToggle     aiToggleStore   // per-chat AI auto-response toggle; non-nil after Start
	cmdDedupe    cmdDedupeStore  // deduplicates toggle command confirmations on redelivery; non-nil after Start
	mu           sync.Mutex
	runCtx       context.Context
	runCancel    context.CancelFunc
	reconnectMu  sync.Mutex
	reconnecting bool
	stopping     atomic.Bool    // set once Stop begins; prevents new wg.Add calls
	wg           sync.WaitGroup // tracks background goroutines (QR handler, reconnect)
}

// NewWhatsAppNativeChannel creates a WhatsApp channel that uses whatsmeow for connection.
// storePath is the directory for the SQLite session store (e.g. workspace/whatsapp).
func NewWhatsAppNativeChannel(
	bc *config.Channel,
	name string,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
	storePath string,
) (channels.Channel, error) {
	base := channels.NewBaseChannel(name, cfg, bus, bc.AllowFrom, channels.WithMaxMessageLength(65536))
	if storePath == "" {
		storePath = "whatsapp"
	}
	c := &WhatsAppNativeChannel{
		BaseChannel: base,
		config:      cfg,
		storePath:   storePath,
	}
	return c, nil
}

func (c *WhatsAppNativeChannel) Start(ctx context.Context) error {
	logger.InfoCF("whatsapp", "Starting WhatsApp native channel (whatsmeow)", map[string]any{"store": c.storePath})

	// Reset lifecycle state from any previous Stop() so a restarted channel
	// behaves correctly.  Use reconnectMu to be consistent with eventHandler
	// and Stop() which coordinate under the same lock.
	c.reconnectMu.Lock()
	c.stopping.Store(false)
	c.reconnecting = false
	c.reconnectMu.Unlock()

	if err := os.MkdirAll(c.storePath, 0o700); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}

	dbPath := filepath.Join(c.storePath, whatsappDBName)
	connStr := "file:" + dbPath + "?_foreign_keys=on"

	db, err := sql.Open(sqliteDriver, connStr)
	if err != nil {
		return fmt.Errorf("open whatsapp store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	waLogger := waLog.Stdout("WhatsApp", "WARN", true)
	container := sqlstore.NewWithDB(db, sqliteDriver, waLogger)
	if err = container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("open whatsapp store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return fmt.Errorf("get device store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, waLogger)

	// Create runCtx/runCancel BEFORE registering event handler and starting
	// goroutines so that Stop() can cancel them at any time, including during
	// the QR-login flow.
	c.runCtx, c.runCancel = context.WithCancel(ctx)

	client.AddEventHandler(c.eventHandler)

	if c.config != nil && c.config.AllowInitialDirectReply {
		store, err := newDirectSeenStore(db)
		if err != nil {
			logger.WarnCF("whatsapp", "Failed to init direct_seen store; allow_initial_direct_reply disabled", map[string]any{"err": err.Error()})
		} else {
			c.directSeen = store
		}
	}

	toggleStore, err := newAIToggleStore(db)
	if err != nil {
		logger.WarnCF("whatsapp", "Failed to init ai_toggle store; AI toggle commands disabled", map[string]any{"err": err.Error()})
	} else {
		c.aiToggle = toggleStore
	}

	dedupeStore, err := newCmdDedupeStore(db)
	if err != nil {
		logger.WarnCF("whatsapp", "Failed to init cmd_dedupe store; command dedup disabled", map[string]any{"err": err.Error()})
	} else {
		c.cmdDedupe = dedupeStore
	}

	c.mu.Lock()
	c.container = container
	c.client = client
	c.mu.Unlock()

	// cleanupOnError clears struct references and releases resources when
	// Start() fails after fields are already assigned.  This prevents
	// Stop() from operating on stale references (double-close, disconnect
	// of a partially-initialized client, or stray event handler callbacks).
	startOK := false
	defer func() {
		if startOK {
			return
		}
		c.runCancel()
		client.Disconnect()
		c.mu.Lock()
		c.client = nil
		c.container = nil
		c.mu.Unlock()
		_ = container.Close()
	}()

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(c.runCtx)
		if err != nil {
			return fmt.Errorf("get QR channel: %w", err)
		}
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		// Handle QR events in a background goroutine so Start() returns
		// promptly.  The goroutine is tracked via c.wg and respects
		// c.runCtx for cancellation.
		// Guard wg.Add with reconnectMu + stopping check (same protocol
		// as eventHandler) so a concurrent Stop() cannot enter wg.Wait()
		// while we call wg.Add(1).
		c.reconnectMu.Lock()
		if c.stopping.Load() {
			c.reconnectMu.Unlock()
			return fmt.Errorf("channel stopped during QR setup")
		}
		c.wg.Add(1)
		c.reconnectMu.Unlock()
		go func() {
			defer c.wg.Done()
			for {
				select {
				case <-c.runCtx.Done():
					return
				case evt, ok := <-qrChan:
					if !ok {
						return
					}
					if evt.Event == "code" {
						logger.InfoCF("whatsapp", "Scan this QR code with WhatsApp (Linked Devices):", nil)
						qrterminal.GenerateWithConfig(evt.Code, qrterminal.Config{
							Level:      qrterminal.L,
							Writer:     os.Stdout,
							HalfBlocks: true,
						})
					} else {
						logger.InfoCF("whatsapp", "WhatsApp login event", map[string]any{"event": evt.Event})
					}
				}
			}
		}()
	} else {
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	}

	startOK = true
	c.SetRunning(true)
	logger.InfoC("whatsapp", "WhatsApp native channel connected")
	return nil
}

func (c *WhatsAppNativeChannel) Stop(ctx context.Context) error {
	logger.InfoC("whatsapp", "Stopping WhatsApp native channel")

	// Mark as stopping under reconnectMu so the flag is visible to
	// eventHandler atomically with respect to its wg.Add(1) call.
	// This closes the TOCTOU window where eventHandler could check
	// stopping (false), then Stop sets it true + enters wg.Wait,
	// then eventHandler calls wg.Add(1) — causing a panic.
	c.reconnectMu.Lock()
	c.stopping.Store(true)
	c.reconnectMu.Unlock()

	if c.runCancel != nil {
		c.runCancel()
	}

	// Disconnect the client first so any blocking Connect()/reconnect loops
	// can be interrupted before we wait on the goroutines.
	c.mu.Lock()
	client := c.client
	container := c.container
	c.mu.Unlock()

	if client != nil {
		client.Disconnect()
	}

	// Wait for background goroutines (QR handler, reconnect) to finish in a
	// context-aware way so Stop can be bounded by ctx.
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines have finished.
	case <-ctx.Done():
		// Context canceled or timed out; log and proceed with best-effort cleanup.
		logger.WarnC("whatsapp", fmt.Sprintf("Stop context canceled before all goroutines finished: %v", ctx.Err()))
	}

	// Now it is safe to clear and close resources.
	c.mu.Lock()
	c.client = nil
	c.container = nil
	c.mu.Unlock()

	if container != nil {
		_ = container.Close() // also closes the underlying *sql.DB
	}
	c.SetRunning(false)
	return nil
}

func (c *WhatsAppNativeChannel) eventHandler(evt any) {
	switch e := evt.(type) {
	case *events.Message:
		c.handleIncoming(e)
	case *events.HistorySync:
		c.handleHistorySync(e)
	case *events.Disconnected:
		logger.InfoCF("whatsapp", "WhatsApp disconnected, will attempt reconnection", nil)
		c.reconnectMu.Lock()
		if c.reconnecting {
			c.reconnectMu.Unlock()
			return
		}
		// Check stopping while holding the lock so the check and wg.Add
		// are atomic with respect to Stop() setting the flag + calling
		// wg.Wait(). This prevents the TOCTOU race.
		if c.stopping.Load() {
			c.reconnectMu.Unlock()
			return
		}
		c.reconnecting = true
		c.wg.Add(1)
		c.reconnectMu.Unlock()
		go func() {
			defer c.wg.Done()
			c.reconnectWithBackoff()
		}()
	}
}

func (c *WhatsAppNativeChannel) handleHistorySync(evt *events.HistorySync) {
	if evt == nil || evt.Data == nil || c.directSeen == nil {
		return
	}
	count, err := c.directSeen.seedFromHistorySync(evt.Data)
	if err != nil {
		logger.WarnCF("whatsapp", "direct_seen history sync seed failed", map[string]any{"err": err.Error()})
		return
	}
	if count > 0 {
		logger.DebugCF("whatsapp", "Seeded direct_seen from WhatsApp history sync", map[string]any{"direct_conversations": count})
	}
}

func (c *WhatsAppNativeChannel) reconnectWithBackoff() {
	defer func() {
		c.reconnectMu.Lock()
		c.reconnecting = false
		c.reconnectMu.Unlock()
	}()

	backoff := reconnectInitial
	for {
		select {
		case <-c.runCtx.Done():
			return
		default:
		}

		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			return
		}

		logger.InfoCF("whatsapp", "WhatsApp reconnecting", map[string]any{"backoff": backoff.String()})
		err := client.Connect()
		if err == nil {
			logger.InfoC("whatsapp", "WhatsApp reconnected")
			return
		}

		logger.WarnCF("whatsapp", "WhatsApp reconnect failed", map[string]any{"error": err.Error()})

		select {
		case <-c.runCtx.Done():
			return
		case <-time.After(backoff):
			if backoff < reconnectMax {
				next := time.Duration(float64(backoff) * reconnectMultiplier)
				if next > reconnectMax {
					next = reconnectMax
				}
				backoff = next
			}
		}
	}
}

func (c *WhatsAppNativeChannel) handleIncoming(evt *events.Message) {
	if evt.Message == nil {
		return
	}
	// Drop outgoing messages: group echoes and DMs sent from the linked account to others.
	// Self-chat (Chat.User == Sender.User) is kept so the account owner can interact with the agent.
	if evt.Info.IsFromMe && evt.Info.Chat.User != evt.Info.Sender.User {
		return
	}
	senderID := evt.Info.Sender.String()
	chatID := evt.Info.Chat.String()
	content := evt.Message.GetConversation()
	if content == "" && evt.Message.ExtendedTextMessage != nil {
		content = evt.Message.ExtendedTextMessage.GetText()
	}
	content = utils.SanitizeMessageContent(content)

	if content == "" {
		return
	}

	var mediaPaths []string

	metadata := make(map[string]string)
	metadata["message_id"] = evt.Info.ID
	if evt.Info.PushName != "" {
		metadata["user_name"] = evt.Info.PushName
	}
	isGroup := evt.Info.Chat.Server == types.GroupServer
	peerKind := "direct"
	if isGroup {
		metadata["peer_kind"] = "group"
		metadata["peer_id"] = chatID
		peerKind = "group"
	} else {
		metadata["peer_kind"] = "direct"
		metadata["peer_id"] = chatID
	}

	messageID := evt.Info.ID
	sender := bus.SenderInfo{
		Platform:    "whatsapp",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("whatsapp", senderID),
		DisplayName: evt.Info.PushName,
	}

	if !c.IsAllowedSender(sender) {
		fields := map[string]any{
			"sender_jid":  senderID,
			"sender_user": evt.Info.Sender.User,
		}
		if !evt.Info.SenderAlt.IsEmpty() {
			fields["sender_alt_jid"] = evt.Info.SenderAlt.String()
			fields["sender_alt_user"] = evt.Info.SenderAlt.User
		}
		logger.WarnCF("whatsapp", "Message rejected by allowlist", fields)
		return
	}

	// Apply the group trigger-name gate before processing toggle commands so
	// that /ai on/off cannot bypass the trigger requirement in group chats.
	if isGroup && shouldDropGroupMessageForTriggerName(groupTriggerName(c.config), content) {
		logger.DebugCF("whatsapp", "WhatsApp message ignored: trigger name absent", map[string]any{
			"chat": chatID,
		})
		return
	}

	if c.aiToggle != nil {
		// Strip trigger-name prefix for command parsing whenever a trigger name
		// is configured and required for this message type (groups always;
		// direct chats when RequireTriggerNameInDirect=true).
		cmdContent := content
		if tn := groupTriggerName(c.config); tn != "" && shouldRequireTriggerName(isGroup, c.config) {
			cmdContent = stripTriggerNamePrefix(tn, content)
		}
		if enable, isCmd := parseAIToggleCommand(cmdContent); isCmd {
			// Check dedupe before mutating state so a redelivered command
			// is silently skipped without re-applying state.
			if c.cmdDedupe != nil {
				seen, dedupeErr := c.cmdDedupe.isSeen(chatID, messageID)
				if dedupeErr != nil {
					logger.WarnCF("whatsapp", "cmd_dedupe check failed", map[string]any{"err": dedupeErr.Error()})
				} else if seen {
					return
				}
			}
			// Mutate state first; only record dedupe and send confirmation on success.
			if err := c.aiToggle.setEnabled(chatID, enable); err != nil {
				logger.WarnCF("whatsapp", "ai_toggle set failed", map[string]any{"err": err.Error()})
				c.mu.Lock()
				client := c.client
				c.mu.Unlock()
				if client != nil && client.IsConnected() {
					if to, err2 := parseJID(chatID); err2 == nil {
						errMsg := &waE2E.Message{Conversation: proto.String("Failed to update AI toggle state. Please try again.")}
						if _, err2 = client.SendMessage(c.runCtx, to, errMsg); err2 != nil {
							logger.WarnCF("whatsapp", "ai_toggle error reply send failed", map[string]any{"err": err2.Error()})
						}
					}
				}
				return
			}
			// State persisted; record dedupe so redelivery skips the confirmation.
			if c.cmdDedupe != nil {
				if err := c.cmdDedupe.markSeen(chatID, messageID); err != nil {
					logger.WarnCF("whatsapp", "cmd_dedupe mark failed", map[string]any{"err": err.Error()})
				}
			}
			reply := "AI responses disabled for this chat."
			if enable {
				reply = "AI responses enabled for this chat."
			}
			c.mu.Lock()
			client := c.client
			c.mu.Unlock()
			if client != nil && client.IsConnected() {
				if to, err := parseJID(chatID); err == nil {
					waMsg := &waE2E.Message{Conversation: proto.String(reply)}
					if _, err := client.SendMessage(c.runCtx, to, waMsg); err != nil {
						logger.WarnCF("whatsapp", "ai_toggle confirmation send failed", map[string]any{"err": err.Error()})
					}
				}
			}
			return
		}

		if enabled, err := c.aiToggle.isEnabled(chatID); err != nil {
			logger.WarnCF("whatsapp", "ai_toggle check failed", map[string]any{"err": err.Error()})
		} else if !enabled {
			logger.DebugCF("whatsapp", "AI disabled for chat, dropping message", map[string]any{"chat": chatID})
			return
		}
	}

	skipTriggerCheck := false
	if !isGroup && c.config != nil && c.config.AllowInitialDirectReply && c.directSeen != nil && shouldRequireTriggerName(false, c.config) {
		consumed, err := c.directSeen.consumeInitialDirectReply(chatID)
		if err != nil {
			logger.WarnCF("whatsapp", "direct_seen check failed", map[string]any{"err": err.Error()})
		} else if consumed {
			skipTriggerCheck = true
		}
	}
	if !skipTriggerCheck && !isGroup && shouldRequireTriggerName(false, c.config) && shouldDropGroupMessageForTriggerName(groupTriggerName(c.config), content) {
		logger.DebugCF("whatsapp", "WhatsApp message ignored: trigger name absent", map[string]any{
			"chat": chatID,
		})
		return
	}

	logger.DebugCF(
		"whatsapp",
		"WhatsApp message received",
		map[string]any{"sender_id": senderID, "content_preview": utils.Truncate(content, 50)},
	)

	inboundCtx := bus.InboundContext{
		Channel:   "whatsapp",
		ChatID:    chatID,
		SenderID:  senderID,
		MessageID: messageID,
		ChatType:  peerKind,
		Raw:       metadata,
	}

	c.HandleInboundContext(c.runCtx, chatID, content, mediaPaths, inboundCtx, sender)
}

func (c *WhatsAppNativeChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("whatsapp connection not established: %w", channels.ErrTemporary)
	}

	// Detect unpaired state: the client is connected (to WhatsApp servers)
	// but has not completed QR-login yet, so sending would fail.
	if client.Store.ID == nil {
		return nil, fmt.Errorf("whatsapp not yet paired (QR login pending): %w", channels.ErrTemporary)
	}

	to, err := parseJID(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat id %q: %w", msg.ChatID, err)
	}

	waMsg := &waE2E.Message{
		Conversation: proto.String(msg.Content),
	}

	if _, err = client.SendMessage(ctx, to, waMsg); err != nil {
		return nil, fmt.Errorf("whatsapp send: %w", channels.ErrTemporary)
	}
	return nil, nil
}

// parseJID converts a chat ID (phone number or JID string) to types.JID.
func parseJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, fmt.Errorf("empty chat id")
	}
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	return types.NewJID(s, types.DefaultUserServer), nil
}
