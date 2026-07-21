//go:build whatsapp_native

// PicoClaw - Ultra-lightweight personal AI agent

package whatsapp

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// authenticatedSenderMarker is the trusted Raw key under test.
const authenticatedSenderMarker = bus.RawKeyWhatsAppAuthenticatedSenderE164

// TestHandleIncoming_AuthenticatedSenderE164_DirectPhone verifies the trusted
// marker is populated for a direct phone-JID sender — the case where no linked-
// phone metadata is set (whatsapp_linked_phone_number stays absent).
func TestHandleIncoming_AuthenticatedSenderE164_DirectPhone(t *testing.T) {
	msgBus := bus.NewMessageBus()
	phoneJID := "33695651381@s.whatsapp.net"

	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{phoneJID}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("33695651381", types.DefaultUserServer),
				Chat:   types.NewJID("33695651381", types.DefaultUserServer),
			},
			ID:       "mid-direct",
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}

	ch.handleIncoming(evt)

	select {
	case msg := <-msgBus.InboundChan():
		raw := msg.Context.Raw
		if raw[authenticatedSenderMarker] != "+33695651381" {
			t.Errorf("%s = %q, want +33695651381", authenticatedSenderMarker, raw[authenticatedSenderMarker])
		}
		// The user-facing linked-phone key must remain absent for direct senders.
		if _, ok := raw["whatsapp_linked_phone_number"]; ok {
			t.Errorf("expected no whatsapp_linked_phone_number for direct match, got %q", raw["whatsapp_linked_phone_number"])
		}
	default:
		t.Fatal("expected inbound message")
	}
}

// TestHandleIncoming_AuthenticatedSenderE164_LIDLookup verifies the trusted
// marker is populated for a LID sender whose phone was resolved via LID->PN
// lookup, alongside the preserved linked-phone metadata.
func TestHandleIncoming_AuthenticatedSenderE164_LIDLookup(t *testing.T) {
	msgBus := bus.NewMessageBus()
	phoneJID := "33695651381@s.whatsapp.net"
	lidJID := types.NewJID("lid-abc", types.HiddenUserServer)

	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{phoneJID}),
		runCtx:      context.Background(),
		lidLookupFn: func(lid types.JID) (string, string, string) {
			if lid == lidJID {
				return phoneJID, "found", ""
			}
			return "", "not_found", ""
		},
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: lidJID,
				Chat:   types.NewJID("33695651381", types.DefaultUserServer),
			},
			ID:       "mid-lid",
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}

	ch.handleIncoming(evt)

	select {
	case msg := <-msgBus.InboundChan():
		raw := msg.Context.Raw
		if raw[authenticatedSenderMarker] != "+33695651381" {
			t.Errorf("%s = %q, want +33695651381", authenticatedSenderMarker, raw[authenticatedSenderMarker])
		}
		// Existing linked-phone behavior is preserved for the LID case.
		if raw["whatsapp_linked_phone_number"] != "+33695651381" {
			t.Errorf("whatsapp_linked_phone_number = %q, want +33695651381", raw["whatsapp_linked_phone_number"])
		}
	default:
		t.Fatal("expected inbound message")
	}
}

// TestHandleIncoming_AuthenticatedSenderE164_NonPhoneIdentity verifies the
// trusted marker is NOT populated when the authenticated identity is not a
// phone number (a directly-allowlisted LID sender). Failing closed here keeps
// non-phone identities from ever reaching the exec environment.
func TestHandleIncoming_AuthenticatedSenderE164_NonPhoneIdentity(t *testing.T) {
	msgBus := bus.NewMessageBus()
	lidJID := types.NewJID("lid-abc", types.HiddenUserServer)

	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{lidJID.String()}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: lidJID,
				Chat:   lidJID,
			},
			ID:       "mid-lid-direct",
			PushName: "Bob",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}

	ch.handleIncoming(evt)

	select {
	case msg := <-msgBus.InboundChan():
		raw := msg.Context.Raw
		if v, ok := raw[authenticatedSenderMarker]; ok {
			t.Errorf("expected no %s for non-phone identity, got %q", authenticatedSenderMarker, v)
		}
	default:
		t.Fatal("expected inbound message")
	}
}

// TestAuthenticatedSenderE164_Helper covers the pure derivation helper across
// the phone/LID/non-phone cases.
func TestAuthenticatedSenderE164_Helper(t *testing.T) {
	tests := []struct {
		name        string
		allowed     bus.SenderInfo
		linkedPhone *linkedPhoneInfo
		want        string
	}{
		{
			name:    "direct phone JID",
			allowed: whatsappSenderInfo("33695651381@s.whatsapp.net", "Alice"),
			want:    "+33695651381",
		},
		{
			name:        "linked phone takes precedence",
			allowed:     whatsappSenderInfo("lid-abc@lid", "Alice"),
			linkedPhone: &linkedPhoneInfo{LinkedNumber: "+14155550100"},
			want:        "+14155550100",
		},
		{
			name:    "non-phone identity yields empty",
			allowed: whatsappSenderInfo("lid-abc@lid", "Bob"),
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := authenticatedSenderE164(tc.allowed, tc.linkedPhone)
			if got != tc.want {
				t.Fatalf("authenticatedSenderE164() = %q, want %q", got, tc.want)
			}
		})
	}
}
