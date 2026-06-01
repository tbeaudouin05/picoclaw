//go:build whatsapp_native

package whatsapp

import (
	"context"
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
