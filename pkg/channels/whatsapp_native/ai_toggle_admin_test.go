//go:build whatsapp_native

package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// sentText records one confirmation message dispatched via the sendTextFn seam.
type sentText struct {
	chatID string
	text   string
}

// newAdminToggleChannel builds a channel with in-memory toggle + dedupe stores
// and a capturing sendTextFn so admin confirmations can be observed. The returned
// slice pointer accumulates every text the channel attempts to send.
func newAdminToggleChannel(mb *bus.MessageBus, allowFrom []string) (*WhatsAppNativeChannel, *[]sentText) {
	sent := &[]sentText{}
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, mb, allowFrom),
		aiToggle:    newMemAIToggleStore(),
		cmdDedupe:   newMemCmdDedupeStore(),
		runCtx:      context.Background(),
	}
	ch.sendTextFn = func(chatID, text string) error {
		*sent = append(*sent, sentText{chatID: chatID, text: text})
		return nil
	}
	return ch, sent
}

// makeOutgoingDirectMsg builds an owner-sent (IsFromMe) direct message where the
// linked account (sender) writes into a customer chat (chat != sender).
func makeOutgoingDirectMsg(owner, customer, content, id string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID(owner, types.DefaultUserServer),
				Chat:     types.NewJID(customer, types.DefaultUserServer),
			},
			ID:       id,
			PushName: owner,
		},
		Message: &waE2E.Message{Conversation: proto.String(content)},
	}
}

// TestAdminToggle_OwnerOutgoingDirectPausesAI verifies the happy path: the owner
// sends "/ai off-admin" into a customer chat. The command is not forwarded, the
// customer chat's AI is disabled, and the exact confirmation is sent into that
// chat. A subsequent inbound customer message is then suppressed.
func TestAdminToggle_OwnerOutgoingDirectPausesAI(t *testing.T) {
	mb := bus.NewMessageBus()
	// Allow the customer so a non-suppressed inbound message would otherwise pass.
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))

	// The admin command must never reach the agent.
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("admin command must not be forwarded, got %q", msg.Content)
	default:
	}

	// Exactly one confirmation, into the customer chat, with the exact text.
	if len(*sent) != 1 {
		t.Fatalf("expected 1 confirmation, got %d: %+v", len(*sent), *sent)
	}
	if got := (*sent)[0].chatID; got != "cust@s.whatsapp.net" {
		t.Fatalf("confirmation chatID=%q, want customer chat", got)
	}
	if got := (*sent)[0].text; got != "AI replies paused — a human will continue this conversation." {
		t.Fatalf("off confirmation text=%q", got)
	}

	// AI must now be disabled for the customer chat: inbound customer message suppressed.
	ch.handleIncoming(makeDirectMsg("cust", "are you there?", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected customer message suppressed after /ai off-admin, got %q", msg.Content)
	default:
	}
}

// TestAdminToggle_OwnerOutgoingDirectResumesAI verifies "/ai on-admin" re-enables
// AI for the customer chat and sends the exact resume confirmation.
func TestAdminToggle_OwnerOutgoingDirectResumesAI(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	// Pause, then resume.
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai on-admin", "m2"))

	if len(*sent) != 2 {
		t.Fatalf("expected 2 confirmations, got %d: %+v", len(*sent), *sent)
	}
	if got := (*sent)[1].text; got != "AI replies resumed." {
		t.Fatalf("on confirmation text=%q", got)
	}

	// Neither command reached the agent.
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("admin command must not be forwarded, got %q", msg.Content)
	default:
	}

	// AI re-enabled: inbound customer message forwarded.
	ch.handleIncoming(makeDirectMsg("cust", "hello again", "m3"))
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected customer message forwarded after /ai on-admin")
	} else if msg.Content != "hello again" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// TestAdminToggle_OwnerNonAdminOutgoingDirectDropped verifies that an ordinary
// owner-sent outgoing DM (not an admin command) is dropped as before: not
// forwarded, no confirmation, and no toggle state change.
func TestAdminToggle_OwnerNonAdminOutgoingDirectDropped(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "hey there, following up", "m1"))

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected ordinary outgoing DM to be dropped, got %q", msg.Content)
	default:
	}
	if len(*sent) != 0 {
		t.Fatalf("expected no confirmation for non-admin message, got %+v", *sent)
	}

	// AI state untouched: a subsequent inbound customer message is forwarded.
	ch.handleIncoming(makeDirectMsg("cust", "hi", "m2"))
	if _, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected customer message forwarded: non-admin outgoing DM must not change AI state")
	}
}

// TestAdminToggle_NearMissAdminCommandDropped verifies a message that resembles
// but is not exactly an admin command (e.g. extra text) is not treated as one.
func TestAdminToggle_NearMissAdminCommandDropped(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "please /ai off-admin now", "m1"))

	if len(*sent) != 0 {
		t.Fatalf("expected no confirmation for near-miss command, got %+v", *sent)
	}
	// AI state untouched.
	ch.handleIncoming(makeDirectMsg("cust", "hi", "m2"))
	if _, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected customer message forwarded: near-miss command must not disable AI")
	}
}

// TestAdminToggle_CustomerCannotUseAdminCommand verifies that a customer (a
// non-owner, inbound IsFromMe=false message) sending "/ai off-admin" gets no
// admin privileges: no confirmation is sent, AI stays enabled, and the message
// itself is forwarded to the agent as ordinary content (not consumed).
func TestAdminToggle_CustomerCannotUseAdminCommand(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	// Inbound message from the customer themselves (IsFromMe=false).
	ch.handleIncoming(makeDirectMsg("cust", "/ai off-admin", "m1"))

	// No admin confirmation may be sent.
	if len(*sent) != 0 {
		t.Fatalf("customer must not trigger admin confirmation, got %+v", *sent)
	}

	// The message is not a recognized command for a customer, so it is forwarded
	// verbatim to the agent.
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected customer's /ai off-admin to be forwarded as ordinary content")
	} else if msg.Content != "/ai off-admin" {
		t.Fatalf("content=%q", msg.Content)
	}

	// AI must remain enabled: a follow-up customer message is still forwarded.
	ch.handleIncoming(makeDirectMsg("cust", "hello", "m2"))
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected follow-up forwarded: customer must not be able to disable AI")
	} else if msg.Content != "hello" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// TestAdminToggle_NotSupportedInGroups verifies the admin command is ignored in
// group chats: an owner echo of "/ai off-admin" into a group is dropped with no
// confirmation and no toggle applied.
func TestAdminToggle_NotSupportedInGroups(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, nil)

	// Owner echo into a group (IsFromMe=true, group chat).
	groupEcho := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("owner", types.DefaultUserServer),
				Chat:     types.NewJID("group1", types.GroupServer),
			},
			ID:       "m1",
			PushName: "owner",
		},
		Message: &waE2E.Message{Conversation: proto.String("/ai off-admin")},
	}
	ch.handleIncoming(groupEcho)

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("group echo must be dropped, got %q", msg.Content)
	default:
	}
	if len(*sent) != 0 {
		t.Fatalf("admin command must not be honored in groups, got %+v", *sent)
	}

	// The group AI must still be enabled: a normal group message is forwarded.
	ch.handleIncoming(makeGroupMsg("group1", "u1", "hello group", "m2"))
	if _, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected group message forwarded: admin command must not disable group AI")
	}
}

// TestAdminToggle_Dedupe verifies that redelivering the same admin command message
// ID does not re-apply the toggle nor re-send the confirmation.
func TestAdminToggle_Dedupe(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))
	// Re-enable with a distinct message id.
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai on-admin", "m2"))
	// Redeliver the original off command (same id m1): must be ignored.
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))

	// Only two confirmations (off m1, on m2); the redelivery sent nothing.
	if len(*sent) != 2 {
		t.Fatalf("expected 2 confirmations (redelivery deduped), got %d: %+v", len(*sent), *sent)
	}

	// AI stayed enabled (redelivered off did not re-apply).
	ch.handleIncoming(makeDirectMsg("cust", "hello", "m3"))
	if msg, ok := recvWithTimeout(mb.InboundChan()); !ok {
		t.Fatal("expected message forwarded: redelivered /ai off-admin must not re-disable AI")
	} else if msg.Content != "hello" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// TestAdminToggle_CaseInsensitiveAndTrimmed verifies the admin command is matched
// case-insensitively after trimming surrounding whitespace.
func TestAdminToggle_CaseInsensitiveAndTrimmed(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "  /AI OFF-ADMIN  ", "m1"))

	if len(*sent) != 1 || (*sent)[0].text != "AI replies paused — a human will continue this conversation." {
		t.Fatalf("expected off confirmation for trimmed/upper command, got %+v", *sent)
	}
	ch.handleIncoming(makeDirectMsg("cust", "hi", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected message suppressed after admin off, got %q", msg.Content)
	default:
	}
}

// TestAdminToggle_NotSupportedInNonUserNonGroupChat verifies that the admin command
// is dropped for non-user non-group servers (e.g. newsletters): no confirmation is
// sent, no toggle state is applied, and the message is not forwarded.
func TestAdminToggle_NotSupportedInNonUserNonGroupChat(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, nil)

	newsletterEcho := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("owner", types.DefaultUserServer),
				Chat:     types.NewJID("channel123", types.NewsletterServer),
			},
			ID:       "m1",
			PushName: "owner",
		},
		Message: &waE2E.Message{Conversation: proto.String("/ai off-admin")},
	}
	ch.handleIncoming(newsletterEcho)

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("newsletter echo must be dropped, got %q", msg.Content)
	default:
	}
	if len(*sent) != 0 {
		t.Fatalf("admin command must not be honored in newsletter chats, got %+v", *sent)
	}
}

// TestAdminToggle_HiddenUserServerRecipientAltUsedWithoutLIDLookup verifies that
// when the outgoing chat JID is HiddenUserServer but RecipientAlt already carries
// a valid DefaultUserServer JID, normalizeAdminTargetChatID uses the RecipientAlt
// PN directly — the LID lookup function must never be invoked. The confirmation
// and subsequent writes use the RecipientAlt PN target.
func TestAdminToggle_HiddenUserServerRecipientAltUsedWithoutLIDLookup(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"1001@s.whatsapp.net"})

	// Panic if LID lookup is called: RecipientAlt must be used instead.
	ch.lidLookupFn = func(lid types.JID) (string, string, string) {
		panic("LID lookup must not be called when RecipientAlt is a valid DefaultUserServer JID")
	}

	adminMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe:     true,
				Sender:       types.NewJID("owner", types.DefaultUserServer),
				Chat:         types.NewJID("cust-lid", types.HiddenUserServer),
				RecipientAlt: types.NewJID("1001", types.DefaultUserServer),
			},
			ID:       "m1",
			PushName: "owner",
		},
		Message: &waE2E.Message{Conversation: proto.String("/ai off-admin")},
	}
	ch.handleIncoming(adminMsg)

	// Confirmation must be addressed to the RecipientAlt PN JID, not the LID.
	if len(*sent) != 1 || (*sent)[0].chatID != "1001@s.whatsapp.net" {
		t.Fatalf("expected confirmation to RecipientAlt PN JID, got %+v", *sent)
	}

	// Inbound message from the PN is suppressed (toggle keyed on PN JID).
	ch.handleIncoming(makeDirectMsg("1001", "still there?", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected customer message suppressed after admin off via RecipientAlt, got %q", msg.Content)
	default:
	}
}

// TestAdminToggle_LIDChatNormalizedToPN verifies that when the customer chat is a
// LID, the toggle is applied under the resolved phone-number JID so that a later
// inbound message from that customer (resolved to the same PN) is suppressed.
func TestAdminToggle_LIDChatNormalizedToPN(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"1001@s.whatsapp.net"})
	ch.lidLookupFn = func(lid types.JID) (string, string, string) {
		if lid == types.NewJID("cust-lid", types.HiddenUserServer) {
			return "1001@s.whatsapp.net", "found", ""
		}
		return "", "not_found", ""
	}

	// Owner sends /ai off-admin into the customer's LID chat.
	adminMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("owner", types.DefaultUserServer),
				Chat:     types.NewJID("cust-lid", types.HiddenUserServer),
			},
			ID:       "m1",
			PushName: "owner",
		},
		Message: &waE2E.Message{Conversation: proto.String("/ai off-admin")},
	}
	ch.handleIncoming(adminMsg)

	// Confirmation is addressed to the resolved PN JID.
	if len(*sent) != 1 || (*sent)[0].chatID != "1001@s.whatsapp.net" {
		t.Fatalf("expected confirmation to resolved PN chat, got %+v", *sent)
	}

	// Inbound message from the customer LID (sender resolves to same PN) is suppressed.
	inbound := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("cust-lid", types.HiddenUserServer),
				Chat:   types.NewJID("cust-lid", types.HiddenUserServer),
			},
			ID:       "m2",
			PushName: "Cust",
		},
		Message: &waE2E.Message{Conversation: proto.String("still there?")},
	}
	ch.handleIncoming(inbound)
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected LID customer message suppressed after admin off, got %q", msg.Content)
	default:
	}
}

// TestAdminToggle_ConfirmationSendFailureRetriedOnRedelivery verifies that when
// the admin confirmation send fails, the command is NOT marked as processed, so
// a redelivery of the same event retries the confirmation. It asserts the retry
// produces exactly one successful confirmation and the final toggle state, and
// that a further redelivery after success is deduped.
func TestAdminToggle_ConfirmationSendFailureRetriedOnRedelivery(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, []string{"cust@s.whatsapp.net"})

	// The first send fails (e.g. disconnected); later sends succeed and are recorded.
	failNext := true
	ch.sendTextFn = func(chatID, text string) error {
		if failNext {
			failNext = false
			return errors.New("disconnected")
		}
		*sent = append(*sent, sentText{chatID: chatID, text: text})
		return nil
	}

	// First delivery: send fails, so no successful confirmation and no dedupe mark.
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))
	if len(*sent) != 0 {
		t.Fatalf("expected no successful confirmation on first (failed) send, got %+v", *sent)
	}
	if seen, _ := ch.cmdDedupe.isSeen("cust@s.whatsapp.net", "m1"); seen {
		t.Fatal("command must not be marked processed after a failed confirmation send")
	}

	// Redelivery of the same event id: dedupe was not marked, so it retries.
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))
	if len(*sent) != 1 {
		t.Fatalf("expected exactly one successful confirmation after retry, got %d: %+v", len(*sent), *sent)
	}
	if got := (*sent)[0].chatID; got != "cust@s.whatsapp.net" {
		t.Fatalf("confirmation chatID=%q, want customer chat", got)
	}
	if got := (*sent)[0].text; got != "AI replies paused — a human will continue this conversation." {
		t.Fatalf("off confirmation text=%q", got)
	}

	// Final toggle state applied: inbound customer message suppressed.
	ch.handleIncoming(makeDirectMsg("cust", "are you there?", "m2"))
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected customer message suppressed after successful retry, got %q", msg.Content)
	default:
	}

	// A further redelivery after success is now deduped: no additional confirmation.
	ch.handleIncoming(makeOutgoingDirectMsg("owner", "cust", "/ai off-admin", "m1"))
	if len(*sent) != 1 {
		t.Fatalf("expected redelivery after success to be deduped, got %d: %+v", len(*sent), *sent)
	}
}

// TestAdminToggle_UnresolvedLIDNotPersistedOrConfirmed verifies that an admin
// command in a HiddenUserServer (LID) customer chat that cannot be resolved to a
// phone-number JID (no valid RecipientAlt, lookup fails) results in no AI-toggle
// state write, no dedupe mark, and no success confirmation.
func TestAdminToggle_UnresolvedLIDNotPersistedOrConfirmed(t *testing.T) {
	mb := bus.NewMessageBus()
	ch, sent := newAdminToggleChannel(mb, nil)

	// LID lookup fails: the target cannot be resolved to a phone-number JID.
	ch.lidLookupFn = func(lid types.JID) (string, string, string) {
		return "", "not_found", ""
	}

	adminMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("owner", types.DefaultUserServer),
				Chat:     types.NewJID("cust-lid", types.HiddenUserServer),
			},
			ID:       "m1",
			PushName: "owner",
		},
		Message: &waE2E.Message{Conversation: proto.String("/ai off-admin")},
	}
	ch.handleIncoming(adminMsg)

	// No success confirmation may be sent.
	if len(*sent) != 0 {
		t.Fatalf("expected no confirmation when LID target is unresolved, got %+v", *sent)
	}

	// No AI-toggle state write.
	toggle, ok := ch.aiToggle.(*memAIToggleStore)
	if !ok {
		t.Fatalf("expected memAIToggleStore, got %T", ch.aiToggle)
	}
	if len(toggle.state) != 0 {
		t.Fatalf("expected no AI-toggle state write for unresolved LID, got %+v", toggle.state)
	}

	// No dedupe mark.
	dedupe, ok := ch.cmdDedupe.(*memCmdDedupeStore)
	if !ok {
		t.Fatalf("expected memCmdDedupeStore, got %T", ch.cmdDedupe)
	}
	if len(dedupe.seen) != 0 {
		t.Fatalf("expected no dedupe mark for unresolved LID, got %+v", dedupe.seen)
	}
}
