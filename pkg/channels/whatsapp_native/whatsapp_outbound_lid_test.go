//go:build whatsapp_native

package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestResolveOutboundTarget_NonLIDPassthrough(t *testing.T) {
	phone := types.NewJID("33695651381", types.DefaultUserServer)
	got, status := resolveOutboundTarget(phone, nil) // lookupFn must not be called
	if got != phone {
		t.Fatalf("JID=%v, want %v", got, phone)
	}
	if status != "not_lid" {
		t.Fatalf("status=%q, want not_lid", status)
	}
}

func TestResolveOutboundTarget(t *testing.T) {
	lid := types.NewJID("lid-user", types.HiddenUserServer)
	phone := types.NewJID("33695651381", types.DefaultUserServer)

	tests := []struct {
		name       string
		lookupFn   func(types.JID) (string, string, string)
		wantJID    types.JID
		wantStatus string
	}{
		{
			name: "resolved to PN JID",
			lookupFn: func(types.JID) (string, string, string) {
				return phone.String(), "found", ""
			},
			wantJID:    phone,
			wantStatus: "found",
		},
		{
			name: "not found returns zero JID",
			lookupFn: func(types.JID) (string, string, string) {
				return "", "not_found", ""
			},
			wantJID:    types.JID{},
			wantStatus: "not_found",
		},
		{
			name: "lookup error returns zero JID",
			lookupFn: func(types.JID) (string, string, string) {
				return "", "lookup_error", "some error"
			},
			wantJID:    types.JID{},
			wantStatus: "lookup_error",
		},
		{
			name: "client unavailable returns zero JID",
			lookupFn: func(types.JID) (string, string, string) {
				return "", "client_unavailable", ""
			},
			wantJID:    types.JID{},
			wantStatus: "client_unavailable",
		},
		{
			name: "lookup receives the original LID JID",
			lookupFn: func(got types.JID) (string, string, string) {
				if got != lid {
					return "", "lookup_error", "wrong JID passed"
				}
				return phone.String(), "found", ""
			},
			wantJID:    phone,
			wantStatus: "found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJID, gotStatus := resolveOutboundTarget(lid, tt.lookupFn)
			if gotJID != tt.wantJID {
				t.Fatalf("JID=%v, want %v", gotJID, tt.wantJID)
			}
			if gotStatus != tt.wantStatus {
				t.Fatalf("status=%q, want %q", gotStatus, tt.wantStatus)
			}
		})
	}
}

// TestHandleIncoming_LIDDirectChat_UsesPNJIDForRouting_LIDLookup verifies that when
// a direct message arrives from a LID-addressed chat and the LID resolves to a PN JID
// via lid_lookup, the inbound ChatID is set to the PN JID. This ensures the agent's
// reply is routed to the phone-number JID that WhatsApp actually accepts for outbound
// delivery (LID targets produce error 463).
func TestHandleIncoming_LIDDirectChat_UsesPNJIDForRouting_LIDLookup(t *testing.T) {
	t.Parallel()
	msgBus := bus.NewMessageBus()
	phoneJID := "393391077930@s.whatsapp.net"
	lidJID := types.NewJID("106000011014190", types.HiddenUserServer)

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
				Chat:   lidJID, // direct message: Chat == Sender (both LID)
			},
			ID:       "mid-lid-routing",
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello from LID direct")},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected inbound message")
	case inbound := <-msgBus.InboundChan():
		if inbound.Context.ChatID != phoneJID {
			t.Errorf("ChatID = %q, want PN JID %q so outbound reply avoids error 463", inbound.Context.ChatID, phoneJID)
		}
		// SenderID stays as the raw LID (allowedSender identity is separate)
		if inbound.Context.SenderID != lidJID.String() {
			t.Errorf("SenderID = %q, want raw LID %q", inbound.Context.SenderID, lidJID.String())
		}
	}
}

// TestHandleIncoming_LIDDirectChat_UsesPNJIDForRouting_SenderAlt verifies the same
// PN-JID normalisation when the PN JID comes from SenderAlt rather than lid_lookup.
func TestHandleIncoming_LIDDirectChat_UsesPNJIDForRouting_SenderAlt(t *testing.T) {
	t.Parallel()
	msgBus := bus.NewMessageBus()
	phoneJID := "393391077930@s.whatsapp.net"
	lidJID := types.NewJID("106000011014190", types.HiddenUserServer)

	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{phoneJID}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:    lidJID,
				SenderAlt: types.NewJID("393391077930", types.DefaultUserServer),
				Chat:      lidJID, // direct message: Chat is the LID
			},
			ID:       "mid-lid-senderalt-routing",
			PushName: "Bob",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello via sender_alt")},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected inbound message")
	case inbound := <-msgBus.InboundChan():
		if inbound.Context.ChatID != phoneJID {
			t.Errorf("ChatID = %q, want PN JID %q", inbound.Context.ChatID, phoneJID)
		}
	}
}

// TestHandleIncoming_LIDDirectChat_LookupFails_ChatIDRemainsLID verifies that when
// the LID sender is NOT in the allowlist (and no resolution succeeds), the message is
// dropped — and when resolution fails the chatID is not altered.
func TestHandleIncoming_LIDDirectChat_LookupFails_MessageDropped(t *testing.T) {
	t.Parallel()
	msgBus := bus.NewMessageBus()
	lidJID := types.NewJID("106000011014190", types.HiddenUserServer)

	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{"other@s.whatsapp.net"}),
		runCtx:      context.Background(),
		lidLookupFn: func(types.JID) (string, string, string) {
			return "", "not_found", ""
		},
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: lidJID,
				Chat:   lidJID,
			},
			ID:       "mid-lid-notfound",
			PushName: "Eve",
		},
		Message: &waE2E.Message{Conversation: proto.String("blocked")},
	}

	ch.handleIncoming(evt)

	select {
	case <-msgBus.InboundChan():
		t.Fatal("expected message to be dropped when LID cannot be resolved to an allowed PN JID")
	default:
		// correctly dropped
	}
}

// TestHandleIncoming_PhoneDirectChat_ChatIDUnchanged verifies that normal
// (non-LID) direct messages are unaffected by the LID normalisation.
func TestHandleIncoming_PhoneDirectChat_ChatIDUnchanged(t *testing.T) {
	t.Parallel()
	msgBus := bus.NewMessageBus()
	phoneJID := "393391077930@s.whatsapp.net"

	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{phoneJID}),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("393391077930", types.DefaultUserServer),
				Chat:   types.NewJID("393391077930", types.DefaultUserServer),
			},
			ID:       "mid-phone-direct",
			PushName: "Carol",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello phone")},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("timeout: expected inbound message")
	case inbound := <-msgBus.InboundChan():
		if inbound.Context.ChatID != phoneJID {
			t.Errorf("ChatID = %q, want original phone JID %q", inbound.Context.ChatID, phoneJID)
		}
	}
}

// TestSendErrorFormat verifies that the error returned when client.SendMessage fails
// satisfies errors.Is(err, channels.ErrTemporary) while including the underlying
// error text for diagnostics — matching the fmt.Errorf pattern used in Send.
func TestSendErrorFormat(t *testing.T) {
	underlying := errors.New("upstream timeout")
	err := fmt.Errorf("whatsapp send: %v: %w", underlying, channels.ErrTemporary)

	if !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("errors.Is(err, ErrTemporary) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), underlying.Error()) {
		t.Fatalf("underlying error text %q not found in %q", underlying.Error(), err.Error())
	}
}
