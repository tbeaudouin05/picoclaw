//go:build whatsapp_native

package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// failingSetEnabledStore wraps a memAIToggleStore and fails the first call to
// setEnabled, then delegates all subsequent calls. Used to test retry behaviour.
type failingSetEnabledStore struct {
	inner  *memAIToggleStore
	failed bool
}

func (s *failingSetEnabledStore) isEnabled(jid string) (bool, error) {
	return s.inner.isEnabled(jid)
}

func (s *failingSetEnabledStore) setEnabled(jid string, enabled bool) error {
	if !s.failed {
		s.failed = true
		return errors.New("simulated persistence failure")
	}
	return s.inner.setEnabled(jid, enabled)
}

// newToggleChannel builds a minimal WhatsAppNativeChannel with an in-memory
// aiToggleStore for AI toggle tests.
func newToggleChannel(mb *bus.MessageBus) *WhatsAppNativeChannel {
	return &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, mb, nil),
		aiToggle:    newMemAIToggleStore(),
		runCtx:      context.Background(),
	}
}

// newToggleChannelWithConfig builds a channel with specific WhatsApp config,
// an in-memory aiToggleStore, and an in-memory cmdDedupeStore.
func newToggleChannelWithConfig(mb *bus.MessageBus, cfg *config.WhatsAppSettings) *WhatsAppNativeChannel {
	return &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, mb, nil),
		config:      cfg,
		aiToggle:    newMemAIToggleStore(),
		cmdDedupe:   newMemCmdDedupeStore(),
		runCtx:      context.Background(),
	}
}

// makeGroupMsg builds a minimal group events.Message for testing.
func makeGroupMsg(group, user, content, id string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID(user, types.DefaultUserServer),
				Chat:   types.NewJID(group, types.GroupServer),
			},
			ID:       id,
			PushName: user,
		},
		Message: &waE2E.Message{Conversation: proto.String(content)},
	}
}

func makeDirectMsg(user, content, id string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID(user, types.DefaultUserServer),
				Chat:   types.NewJID(user, types.DefaultUserServer),
			},
			ID:       id,
			PushName: user,
		},
		Message: &waE2E.Message{Conversation: proto.String(content)},
	}
}

func recvWithTimeout(ch <-chan bus.InboundMessage) (bus.InboundMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		return bus.InboundMessage{}, false
	case msg, ok := <-ch:
		if !ok {
			return bus.InboundMessage{}, false
		}
		return msg, true
	}
}

// TestAIToggle_DefaultEnabled verifies that messages are forwarded when no
// toggle state has been set for a chat.
func TestAIToggle_DefaultEnabled(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannel(mb)

	ch.handleIncoming(makeDirectMsg("1001", "hello", "m1"))

	if _, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected message to be forwarded when AI is enabled by default")
	}
}

// TestAIToggle_OffSuppressesMessages verifies that normal messages are not
// forwarded after /ai off is sent for the same chat.
func TestAIToggle_OffSuppressesMessages(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannel(mb)

	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))
	// command must not appear on the bus
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected /ai off not to be forwarded, got content %q", msg.Content)
	default:
	}

	ch.handleIncoming(makeDirectMsg("1001", "hello after off", "m2"))

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message to be suppressed after /ai off, got content %q", msg.Content)
	default:
		// suppressed as expected
	}
}

// TestAIToggle_OnReEnables verifies that /ai on re-enables forwarding after
// /ai off.
func TestAIToggle_OnReEnables(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannel(mb)

	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))
	ch.handleIncoming(makeDirectMsg("1001", "/ai on", "m2"))
	// neither command must appear on the bus
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected toggle commands not to be forwarded, got content %q", msg.Content)
	default:
	}

	ch.handleIncoming(makeDirectMsg("1001", "hello after on", "m3"))

	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected message to be forwarded after /ai on")
	} else if msg.Content != "hello after on" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// TestAIToggle_CommandsNotDeliveredToAgent verifies that /ai on and /ai off
// are never published on the inbound bus.
func TestAIToggle_CommandsNotDeliveredToAgent(t *testing.T) {
	for _, cmd := range []string{"/ai off", "/ai on", "  /AI OFF  ", " /Ai On "} {
		mb := bus.NewMessageBus()
		ch := newToggleChannel(mb)

		ch.handleIncoming(makeDirectMsg("1001", cmd, "m1"))

		select {
		case msg := <-mb.InboundChan():
			t.Errorf("command %q must not be forwarded to agent, got content %q", cmd, msg.Content)
		default:
		}
	}
}

// TestAIToggle_StateIsPerChat verifies that disabling AI for one chat does
// not affect other chats.
func TestAIToggle_StateIsPerChat(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannel(mb)

	// Disable AI for chat 1001
	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))

	// Message from chat 1002 should still be forwarded
	ch.handleIncoming(makeDirectMsg("1002", "hello from 1002", "m2"))

	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected message from unaffected chat to be forwarded")
	} else if msg.Content != "hello from 1002" {
		t.Fatalf("content=%q", msg.Content)
	}

	// Message from chat 1001 should still be suppressed
	ch.handleIncoming(makeDirectMsg("1001", "hello from 1001", "m3"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message from disabled chat to be suppressed, got content %q", msg.Content)
	default:
	}
}

// TestAIToggle_GroupToggleRequiresTriggerName verifies that /ai on/off in a
// group is silently dropped when the group trigger name is absent.  The toggle
// must not take effect, and no message must reach the agent.
func TestAIToggle_GroupToggleRequiresTriggerName(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannelWithConfig(mb, &config.WhatsAppSettings{GroupTriggerName: "bot"})

	// /ai off WITHOUT trigger name → dropped; AI state must remain enabled.
	ch.handleIncoming(makeGroupMsg("g1", "u1", "/ai off", "m1"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("toggle command without trigger name must not reach agent, got %q", msg.Content)
	default:
	}

	// Subsequent group message with trigger name must be forwarded (AI still on).
	ch.handleIncoming(makeGroupMsg("g1", "u1", "bot hello", "m2"))
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected group message to be forwarded: /ai off without trigger name must not disable AI")
	} else if msg.Content != "bot hello" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// TestAIToggle_GroupToggleWithTriggerName verifies that /ai off in a group
// is processed when the message also contains the group trigger name.
func TestAIToggle_GroupToggleWithTriggerName(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannelWithConfig(mb, &config.WhatsAppSettings{GroupTriggerName: "bot"})

	// /ai off WITH trigger name → toggle processed; command must not reach agent.
	ch.handleIncoming(makeGroupMsg("g1", "u1", "bot /ai off", "m1"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("toggle command must not be forwarded to agent, got %q", msg.Content)
	default:
	}

	// Subsequent group message with trigger name must be suppressed (AI now off).
	ch.handleIncoming(makeGroupMsg("g1", "u1", "bot hello", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message suppressed after group /ai off, got %q", msg.Content)
	default:
	}
}

// TestAIToggle_DedupCommandConfirmation verifies that redelivering the same
// toggle command message ID does not re-apply the toggle state.
func TestAIToggle_DedupCommandConfirmation(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannelWithConfig(mb, nil)

	// /ai off (m1) → state=disabled
	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))
	// /ai on (m2) → state=enabled
	ch.handleIncoming(makeDirectMsg("1001", "/ai on", "m2"))
	// Redeliver /ai off (m1) → already seen; must NOT re-apply; state stays enabled.
	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))

	// Normal message must be forwarded because AI is still enabled.
	ch.handleIncoming(makeDirectMsg("1001", "hello", "m3"))
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected message forwarded: redelivered /ai off must not re-disable AI")
	} else if msg.Content != "hello" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// TestAIToggle_SetEnabledFailureAllowsRetry verifies that when setEnabled fails
// the command is NOT marked as dedupe-processed, so a redelivery can retry and
// eventually succeed.
func TestAIToggle_SetEnabledFailureAllowsRetry(t *testing.T) {
	mb := bus.NewMessageBus()
	ts := &failingSetEnabledStore{inner: newMemAIToggleStore()}
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, mb, nil),
		aiToggle:    ts,
		cmdDedupe:   newMemCmdDedupeStore(),
		runCtx:      context.Background(),
	}

	// First delivery: setEnabled fails → state unchanged (AI still on), not marked as seen.
	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))

	// Message is still forwarded because state was not changed.
	ch.handleIncoming(makeDirectMsg("1001", "hello after failed toggle", "m2"))
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected message forwarded: setEnabled failure must not change AI state")
	} else if msg.Content != "hello after failed toggle" {
		t.Fatalf("content=%q", msg.Content)
	}

	// Redelivery of /ai off (m1): setEnabled now succeeds → state=disabled, marked as seen.
	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))

	// Message must now be suppressed.
	ch.handleIncoming(makeDirectMsg("1001", "hello after retry", "m3"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message suppressed after successful retry, got %q", msg.Content)
	default:
	}
}

// TestAIToggle_DirectToggleWithTriggerPrefix verifies that in a direct chat with
// RequireTriggerNameInDirect=true, "bot /ai off" is parsed as a toggle command
// (trigger prefix stripped) and does not leak to the agent.
func TestAIToggle_DirectToggleWithTriggerPrefix(t *testing.T) {
	mb := bus.NewMessageBus()
	ch := newToggleChannelWithConfig(mb, &config.WhatsAppSettings{
		GroupTriggerName:           "bot",
		RequireTriggerNameInDirect: true,
	})

	// "bot /ai off" → recognized as toggle command after prefix strip.
	ch.handleIncoming(makeDirectMsg("1001", "bot /ai off", "m1"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("toggle command must not be forwarded to agent, got %q", msg.Content)
	default:
	}

	// Subsequent message must be suppressed (AI is now off).
	ch.handleIncoming(makeDirectMsg("1001", "bot hello", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message suppressed after /ai off, got %q", msg.Content)
	default:
	}
}

// TestAIToggle_DirectBareCommandPreserved verifies that bare "/ai off" still
// works in a direct chat when RequireTriggerNameInDirect is false, regardless
// of whether a GroupTriggerName is configured.
func TestAIToggle_DirectBareCommandPreserved(t *testing.T) {
	mb := bus.NewMessageBus()
	// GroupTriggerName configured, but RequireTriggerNameInDirect is false (default).
	ch := newToggleChannelWithConfig(mb, &config.WhatsAppSettings{GroupTriggerName: "bot"})

	ch.handleIncoming(makeDirectMsg("1001", "/ai off", "m1"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("toggle command must not be forwarded to agent, got %q", msg.Content)
	default:
	}

	ch.handleIncoming(makeDirectMsg("1001", "hello", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message suppressed after /ai off, got %q", msg.Content)
	default:
	}
}
