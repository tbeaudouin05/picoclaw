//go:build whatsapp_native

package whatsapp

import (
	"context"
	"testing"
	"time"

	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	waWeb "go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestHandleIncoming_RejectsNonAllowedSender(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, []string{"9999"}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("1001", types.DefaultUserServer),
				Chat:   types.NewJID("1001", types.DefaultUserServer),
			},
			ID:       "mid2",
			PushName: "Eve",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("should be blocked"),
		},
	}

	ch.handleIncoming(evt)

	select {
	case <-messageBus.InboundChan():
		t.Fatal("expected no message to be forwarded for rejected sender")
	default:
		// rejected as expected
	}
}

func TestHandleIncoming_GroupBotEchoIsDropped(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("bot", types.DefaultUserServer),
				Chat:     types.NewJID("group1", types.GroupServer),
			},
			ID:       "mid-echo",
			PushName: "Bot",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("hello from bot"),
		},
	}

	ch.handleIncoming(evt)

	select {
	case <-messageBus.InboundChan():
		t.Fatal("expected group bot echo to be dropped with no inbound message")
	default:
		// dropped as expected
	}
}

func TestHandleIncoming_GroupUserMessageIsProcessed(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: false,
				Sender:   types.NewJID("1001", types.DefaultUserServer),
				Chat:     types.NewJID("group1", types.GroupServer),
			},
			ID:       "mid-group",
			PushName: "Alice",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("hello group"),
		},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected group message to be processed")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if inbound.Context.ChatType != "group" {
			t.Fatalf("expected ChatType=group, got %q", inbound.Context.ChatType)
		}
		if inbound.Content != "hello group" {
			t.Fatalf("content=%q", inbound.Content)
		}
	}
}

func TestHandleIncoming_DirectOutgoingDMIsDropped(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
		runCtx:      context.Background(),
	}

	// Linked account (thomas) sends a DM to someone else (domenico).
	// IsFromMe=true, Chat.User != Sender.User → must be dropped.
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("thomas", types.DefaultUserServer),
				Chat:     types.NewJID("domenico", types.DefaultUserServer),
			},
			ID:       "mid-outgoing",
			PushName: "Thomas",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("hey domenico"),
		},
	}

	ch.handleIncoming(evt)

	select {
	case <-messageBus.InboundChan():
		t.Fatal("expected outgoing DM from linked account to be dropped")
	default:
		// dropped as expected
	}
}

func TestHandleIncoming_DirectSelfChatIsFromMeIsProcessed(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				IsFromMe: true,
				Sender:   types.NewJID("1001", types.DefaultUserServer),
				Chat:     types.NewJID("1001", types.DefaultUserServer),
			},
			ID:       "mid-self",
			PushName: "Me",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("self note"),
		},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected direct self-chat message to be processed")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if inbound.Content != "self note" {
			t.Fatalf("content=%q", inbound.Content)
		}
	}
}

func TestHandleIncoming_DoesNotConsumeGenericCommandsLocally(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("1001", types.DefaultUserServer),
				Chat:   types.NewJID("1001", types.DefaultUserServer),
			},
			ID:       "mid1",
			PushName: "Alice",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("/new"),
		},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for message to be forwarded")
		return
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		if inbound.Channel != "whatsapp_native" {
			t.Fatalf("channel=%q", inbound.Channel)
		}
		if inbound.Content != "/new" {
			t.Fatalf("content=%q", inbound.Content)
		}
	}
}

func TestHandleIncoming_GroupTriggerName(t *testing.T) {
	tests := []struct {
		name                       string
		groupTriggerName           string
		requireTriggerNameInDirect bool
		chat                       types.JID
		content                    string
		wantForwarded              bool
	}{
		{
			name:             "group drops when trigger name absent",
			groupTriggerName: "Alice",
			chat:             types.NewJID("group1", types.GroupServer),
			content:          "hello everyone",
			wantForwarded:    false,
		},
		{
			name:             "group passes when trigger name present case insensitive",
			groupTriggerName: "Alice",
			chat:             types.NewJID("group1", types.GroupServer),
			content:          "hey alice can you help?",
			wantForwarded:    true,
		},
		{
			name:             "group passes when trigger name present with punctuation",
			groupTriggerName: "Alice",
			chat:             types.NewJID("group1", types.GroupServer),
			content:          "Alice, can you help?",
			wantForwarded:    true,
		},
		{
			name:             "group passes when trigger name unset",
			groupTriggerName: "",
			chat:             types.NewJID("group1", types.GroupServer),
			content:          "hello everyone",
			wantForwarded:    true,
		},
		{
			name:                       "direct bypasses trigger name by default",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: false,
			chat:                       types.NewJID("1001", types.DefaultUserServer),
			content:                    "hello privately",
			wantForwarded:              true,
		},
		{
			name:                       "direct drops when trigger name is required and absent",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			chat:                       types.NewJID("1001", types.DefaultUserServer),
			content:                    "hello privately",
			wantForwarded:              false,
		},
		{
			name:                       "direct passes when trigger name is required and present",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			chat:                       types.NewJID("1001", types.DefaultUserServer),
			content:                    "Alice please respond",
			wantForwarded:              true,
		},
		{
			name:             "substring is not a word match",
			groupTriggerName: "Alice",
			chat:             types.NewJID("group1", types.GroupServer),
			content:          "malice should not trigger",
			wantForwarded:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messageBus := bus.NewMessageBus()
			ch := &WhatsAppNativeChannel{
				BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
				config: &config.WhatsAppSettings{
					GroupTriggerName:           tt.groupTriggerName,
					RequireTriggerNameInDirect: tt.requireTriggerNameInDirect,
				},
				runCtx: context.Background(),
			}

			evt := &events.Message{
				Info: types.MessageInfo{
					MessageSource: types.MessageSource{
						Sender: types.NewJID("1001", types.DefaultUserServer),
						Chat:   tt.chat,
					},
					ID:       "mid-trigger",
					PushName: "Alice",
				},
				Message: &waE2E.Message{
					Conversation: proto.String(tt.content),
				},
			}

			ch.handleIncoming(evt)

			select {
			case inbound := <-messageBus.InboundChan():
				if !tt.wantForwarded {
					t.Fatalf("expected message to be dropped, got inbound content %q", inbound.Content)
				}
				if inbound.Content != tt.content {
					t.Fatalf("content=%q", inbound.Content)
				}
			default:
				if tt.wantForwarded {
					t.Fatal("expected message to be forwarded")
				}
			}
		})
	}
}

func TestHandleIncoming_AllowInitialDirectReply(t *testing.T) {
	tests := []struct {
		name                       string
		groupTriggerName           string
		requireTriggerNameInDirect bool
		chat                       types.JID
		directSeenPre              []string // JIDs pre-seeded as already seen
		content                    string
		wantForwarded              bool
	}{
		{
			name:                       "direct first message bypasses trigger when allow_initial_direct_reply",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			chat:                       types.NewJID("1001", types.DefaultUserServer),
			content:                    "hello without trigger",
			wantForwarded:              true,
		},
		{
			name:                       "direct second message requires trigger when require_trigger_name_in_direct",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			chat:                       types.NewJID("1001", types.DefaultUserServer),
			directSeenPre:              []string{"1001@s.whatsapp.net"},
			content:                    "hello without trigger",
			wantForwarded:              false,
		},
		{
			name:                       "direct second message passes when trigger present",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			chat:                       types.NewJID("1001", types.DefaultUserServer),
			directSeenPre:              []string{"1001@s.whatsapp.net"},
			content:                    "Alice help me",
			wantForwarded:              true,
		},
		{
			name:                       "group first message is unaffected by allow_initial_direct_reply",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: false,
			chat:                       types.NewJID("group1", types.GroupServer),
			content:                    "hello without trigger",
			wantForwarded:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemDirectSeenStore()
			for _, jid := range tt.directSeenPre {
				_ = store.markDirectMessageSeen(jid)
			}

			messageBus := bus.NewMessageBus()
			ch := &WhatsAppNativeChannel{
				BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
				config: &config.WhatsAppSettings{
					GroupTriggerName:           tt.groupTriggerName,
					RequireTriggerNameInDirect: tt.requireTriggerNameInDirect,
					AllowInitialDirectReply:    true,
				},
				directSeen: store,
				runCtx:     context.Background(),
			}

			senderJID := types.NewJID("1001", types.DefaultUserServer)
			evt := &events.Message{
				Info: types.MessageInfo{
					MessageSource: types.MessageSource{
						Sender: senderJID,
						Chat:   tt.chat,
					},
					ID:       "mid-initial",
					PushName: "Alice",
				},
				Message: &waE2E.Message{
					Conversation: proto.String(tt.content),
				},
			}

			ch.handleIncoming(evt)

			select {
			case inbound := <-messageBus.InboundChan():
				if !tt.wantForwarded {
					t.Fatalf("expected message to be dropped, got inbound content %q", inbound.Content)
				}
				if inbound.Content != tt.content {
					t.Fatalf("content=%q", inbound.Content)
				}
			default:
				if tt.wantForwarded {
					t.Fatal("expected message to be forwarded")
				}
			}
		})
	}
}

func TestDirectSeenStoreSeedFromHistorySync(t *testing.T) {
	store := newMemDirectSeenStore()
	sync := &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{
			{
				ID: proto.String("1001@s.whatsapp.net"),
				Messages: []*waHistorySync.HistorySyncMsg{
					{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String("1001@s.whatsapp.net")}}},
				},
			},
			{
				ID: proto.String("group1@g.us"),
				Messages: []*waHistorySync.HistorySyncMsg{
					{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String("group1@g.us")}}},
				},
			},
			{
				ID: proto.String("empty@s.whatsapp.net"),
			},
		},
	}

	count, err := store.seedFromHistorySync(sync)
	if err != nil {
		t.Fatalf("seedFromHistorySync error: %v", err)
	}
	if count != 1 {
		t.Fatalf("seeded count=%d, want 1", count)
	}
	if first, _ := store.consumeInitialDirectReply("1001@s.whatsapp.net"); first {
		t.Fatal("expected historical direct conversation to be marked seen")
	}
	if first, _ := store.consumeInitialDirectReply("group1@g.us"); !first {
		t.Fatal("expected group history not to be marked as direct seen")
	}
	if first, _ := store.consumeInitialDirectReply("empty@s.whatsapp.net"); !first {
		t.Fatal("expected empty direct conversation not to be marked seen")
	}
}

func TestHandleIncoming_AllowsLIDSenderMatchedByPhoneAlt(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, []string{"1001@s.whatsapp.net"}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:    types.NewJID("lid-user", types.HiddenUserServer),
				SenderAlt: types.NewJID("1001", types.DefaultUserServer),
				Chat:      types.NewJID("lid-user", types.HiddenUserServer),
			},
			ID:       "mid-lid-allowed-by-phone",
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello from lid")},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected LID sender to be allowed by phone-number SenderAlt")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if inbound.Content != "hello from lid" {
			t.Fatalf("content=%q", inbound.Content)
		}
		if inbound.Context.SenderID != "lid-user@lid" {
			t.Fatalf("context SenderID=%q, want raw LID sender context", inbound.Context.SenderID)
		}
		if inbound.Sender.PlatformID != "1001@s.whatsapp.net" {
			t.Fatalf("sender PlatformID=%q, want phone-number SenderAlt identity used for allowlist gate", inbound.Sender.PlatformID)
		}
	}
}

func TestHandleIncoming_RejectsLIDSenderWhenPhoneAltNotAllowed(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, []string{"9999@s.whatsapp.net"}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:    types.NewJID("lid-user", types.HiddenUserServer),
				SenderAlt: types.NewJID("1001", types.DefaultUserServer),
				Chat:      types.NewJID("lid-user", types.HiddenUserServer),
			},
			ID:       "mid-lid-rejected-by-phone",
			PushName: "Mallory",
		},
		Message: &waE2E.Message{Conversation: proto.String("blocked lid")},
	}

	ch.handleIncoming(evt)

	select {
	case <-messageBus.InboundChan():
		t.Fatal("expected no message to be forwarded when neither LID nor phone-number SenderAlt is allowed")
	default:
		// rejected as expected
	}
}

func TestHandleIncoming_AllowsLIDSenderByLIDDirectly(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, []string{"lid-user@lid"}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("lid-user", types.HiddenUserServer),
				Chat:   types.NewJID("lid-user", types.HiddenUserServer),
			},
			ID:       "mid-lid-allowed-directly",
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello by lid")},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected LID sender to be allowed directly")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if inbound.Content != "hello by lid" {
			t.Fatalf("content=%q", inbound.Content)
		}
	}
}

func TestHandleIncoming_AllowsPhoneSenderMatchedByLIDAlt(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, []string{"lid-user@lid"}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:    types.NewJID("1001", types.DefaultUserServer),
				SenderAlt: types.NewJID("lid-user", types.HiddenUserServer),
				Chat:      types.NewJID("1001", types.DefaultUserServer),
			},
			ID:       "mid-phone-allowed-by-lid",
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello from phone")},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected phone sender to be allowed by LID SenderAlt")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if inbound.Content != "hello from phone" {
			t.Fatalf("content=%q", inbound.Content)
		}
		if inbound.Context.SenderID != "1001@s.whatsapp.net" {
			t.Fatalf("context SenderID=%q, want raw phone sender context", inbound.Context.SenderID)
		}
		if inbound.Sender.PlatformID != "lid-user@lid" {
			t.Fatalf("sender PlatformID=%q, want LID SenderAlt identity used for allowlist gate", inbound.Sender.PlatformID)
		}
	}
}
