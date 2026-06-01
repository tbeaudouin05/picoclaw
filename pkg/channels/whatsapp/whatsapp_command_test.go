package whatsapp

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestHandleIncomingMessage_DoesNotConsumeGenericCommandsLocally(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp", config.WhatsAppSettings{}, messageBus, nil),
		ctx:         context.Background(),
	}

	ch.handleIncomingMessage(map[string]any{
		"type":    "message",
		"id":      "mid1",
		"from":    "user1",
		"chat":    "chat1",
		"content": "/help",
	})

	inbound, ok := <-messageBus.InboundChan()
	if !ok {
		t.Fatal("expected inbound message to be forwarded")
	}
	if inbound.Channel != "whatsapp" {
		t.Fatalf("channel=%q", inbound.Channel)
	}
	if inbound.Content != "/help" {
		t.Fatalf("content=%q", inbound.Content)
	}
}

func TestHandleIncomingMessage_GroupTriggerName(t *testing.T) {
	tests := []struct {
		name                       string
		groupTriggerName           string
		requireTriggerNameInDirect bool
		from                       string
		chat                       string
		content                    string
		wantForwarded              bool
	}{
		{
			name:             "group drops when trigger name absent",
			groupTriggerName: "Alice",
			from:             "user1",
			chat:             "group1",
			content:          "hello everyone",
			wantForwarded:    false,
		},
		{
			name:             "group passes when trigger name present case insensitive",
			groupTriggerName: "Alice",
			from:             "user1",
			chat:             "group1",
			content:          "hey alice can you help?",
			wantForwarded:    true,
		},
		{
			name:             "group passes when trigger name present with punctuation",
			groupTriggerName: "Alice",
			from:             "user1",
			chat:             "group1",
			content:          "Alice, can you help?",
			wantForwarded:    true,
		},
		{
			name:             "group passes when trigger name unset",
			groupTriggerName: "",
			from:             "user1",
			chat:             "group1",
			content:          "hello everyone",
			wantForwarded:    true,
		},
		{
			name:                       "direct bypasses trigger name by default",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: false,
			from:                       "user1",
			chat:                       "user1",
			content:                    "hello privately",
			wantForwarded:              true,
		},
		{
			name:                       "direct drops when trigger name is required and absent",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			from:                       "user1",
			chat:                       "user1",
			content:                    "hello privately",
			wantForwarded:              false,
		},
		{
			name:                       "direct passes when trigger name is required and present",
			groupTriggerName:           "Alice",
			requireTriggerNameInDirect: true,
			from:                       "user1",
			chat:                       "user1",
			content:                    "Alice please respond",
			wantForwarded:              true,
		},
		{
			name:             "substring is not a word match",
			groupTriggerName: "Alice",
			from:             "user1",
			chat:             "group1",
			content:          "malice should not trigger",
			wantForwarded:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messageBus := bus.NewMessageBus()
			ch := &WhatsAppChannel{
				BaseChannel: channels.NewBaseChannel("whatsapp", config.WhatsAppSettings{}, messageBus, nil),
				config: &config.WhatsAppSettings{
					GroupTriggerName:           tt.groupTriggerName,
					RequireTriggerNameInDirect: tt.requireTriggerNameInDirect,
				},
				ctx: context.Background(),
			}

			ch.handleIncomingMessage(map[string]any{
				"type":    "message",
				"id":      "mid-trigger",
				"from":    tt.from,
				"chat":    tt.chat,
				"content": tt.content,
			})

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
