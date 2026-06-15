//go:build whatsapp_native

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

// --- allowedWhatsAppSender unit tests ---

func newTestChannel(allowList []string) *WhatsAppNativeChannel {
	return &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, bus.NewMessageBus(), allowList),
		runCtx:      context.Background(),
	}
}

func TestAllowedWhatsAppSender_DirectMatch(t *testing.T) {
	ch := newTestChannel([]string{"33695651381@s.whatsapp.net"})
	rawJID := "33695651381@s.whatsapp.net"
	sender := whatsappSenderInfo(rawJID, "Alice")
	senderAlt := types.JID{} // empty

	got, lp, ok := ch.allowedWhatsAppSender(rawJID, sender, senderAlt)
	if !ok {
		t.Fatal("expected allowed")
	}
	if got.PlatformID != rawJID {
		t.Errorf("PlatformID = %q, want %q", got.PlatformID, rawJID)
	}
	if lp != nil {
		t.Errorf("linkedPhoneInfo should be nil for direct match, got %+v", lp)
	}
}

func TestAllowedWhatsAppSender_SenderAlt_PhoneJID(t *testing.T) {
	phoneJID := "33695651381@s.whatsapp.net"
	ch := newTestChannel([]string{phoneJID})

	// Sender is a LID, senderAlt is the phone JID
	lidJID := types.NewJID("lid-abc", types.HiddenUserServer)
	rawJID := lidJID.String()
	sender := whatsappSenderInfo(rawJID, "Alice")
	senderAlt := types.NewJID("33695651381", types.DefaultUserServer)

	got, lp, ok := ch.allowedWhatsAppSender(rawJID, sender, senderAlt)
	if !ok {
		t.Fatal("expected allowed via SenderAlt")
	}
	if got.PlatformID != phoneJID {
		t.Errorf("PlatformID = %q, want %q", got.PlatformID, phoneJID)
	}
	if lp == nil {
		t.Fatal("expected linkedPhoneInfo, got nil")
	}
	if lp.SenderJID != rawJID {
		t.Errorf("SenderJID = %q, want %q", lp.SenderJID, rawJID)
	}
	if lp.LinkedJID != phoneJID {
		t.Errorf("LinkedJID = %q, want %q", lp.LinkedJID, phoneJID)
	}
	if lp.LinkedNumber != "+33695651381" {
		t.Errorf("LinkedNumber = %q, want %q", lp.LinkedNumber, "+33695651381")
	}
	if lp.Source != "sender_alt" {
		t.Errorf("Source = %q, want %q", lp.Source, "sender_alt")
	}
}

func TestAllowedWhatsAppSender_SenderAlt_NonPhoneJID_NoLinkedInfo(t *testing.T) {
	// senderAlt is a group JID (non-phone): linked info must NOT be added
	groupJID := types.NewJID("group123", types.GroupServer)
	groupJIDStr := groupJID.String()
	ch := newTestChannel([]string{groupJIDStr})

	rawJID := "lid-abc@lid.whatsapp.net"
	sender := whatsappSenderInfo(rawJID, "Bob")
	senderAlt := groupJID

	got, lp, ok := ch.allowedWhatsAppSender(rawJID, sender, senderAlt)
	if !ok {
		t.Fatal("expected allowed via SenderAlt")
	}
	if got.PlatformID != groupJIDStr {
		t.Errorf("PlatformID = %q, want %q", got.PlatformID, groupJIDStr)
	}
	if lp != nil {
		t.Errorf("linkedPhoneInfo should be nil for non-phone SenderAlt, got %+v", lp)
	}
}

func TestAllowedWhatsAppSender_LIDLookup(t *testing.T) {
	phoneJID := "33695651381@s.whatsapp.net"
	ch := newTestChannel([]string{phoneJID})

	lidJID := types.NewJID("lid-abc", types.HiddenUserServer)
	rawJID := lidJID.String()
	sender := whatsappSenderInfo(rawJID, "Alice")
	senderAlt := types.JID{} // empty — forces LID lookup path

	ch.lidLookupFn = func(lid types.JID) (string, string, string) {
		if lid == lidJID {
			return phoneJID, "found", ""
		}
		return "", "not_found", ""
	}

	got, lp, ok := ch.allowedWhatsAppSender(rawJID, sender, senderAlt)
	if !ok {
		t.Fatal("expected allowed via LID lookup")
	}
	if got.PlatformID != phoneJID {
		t.Errorf("PlatformID = %q, want %q", got.PlatformID, phoneJID)
	}
	if lp == nil {
		t.Fatal("expected linkedPhoneInfo, got nil")
	}
	if lp.SenderJID != rawJID {
		t.Errorf("SenderJID = %q, want %q", lp.SenderJID, rawJID)
	}
	if lp.LinkedJID != phoneJID {
		t.Errorf("LinkedJID = %q, want %q", lp.LinkedJID, phoneJID)
	}
	if lp.LinkedNumber != "+33695651381" {
		t.Errorf("LinkedNumber = %q, want %q", lp.LinkedNumber, "+33695651381")
	}
	if lp.Source != "lid_lookup" {
		t.Errorf("Source = %q, want %q", lp.Source, "lid_lookup")
	}
}

func TestAllowedWhatsAppSender_NotAllowed(t *testing.T) {
	ch := newTestChannel([]string{"9999@s.whatsapp.net"})
	rawJID := "1111@s.whatsapp.net"
	sender := whatsappSenderInfo(rawJID, "Unknown")
	senderAlt := types.JID{}

	_, lp, ok := ch.allowedWhatsAppSender(rawJID, sender, senderAlt)
	if ok {
		t.Fatal("expected not allowed")
	}
	if lp != nil {
		t.Errorf("linkedPhoneInfo should be nil when not allowed, got %+v", lp)
	}
}

// --- handleIncoming metadata integration tests ---

func TestHandleIncoming_LinkedPhone_SenderAlt_AddsMetadata(t *testing.T) {
	msgBus := bus.NewMessageBus()
	phoneJID := "33695651381@s.whatsapp.net"
	ch := newTestChannel([]string{phoneJID})
	ch.runCtx = context.Background()

	lidJID := types.NewJID("lid-abc", types.HiddenUserServer)

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:    lidJID,
				SenderAlt: types.NewJID("33695651381", types.DefaultUserServer),
				Chat:      types.NewJID("33695651381", types.DefaultUserServer),
			},
			ID:       "mid-lp",
			PushName: "Alice",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("hello"),
		},
	}
	ch.BaseChannel = channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, msgBus, []string{phoneJID})
	ch.runCtx = context.Background()

	ch.handleIncoming(evt)

	select {
	case msg := <-msgBus.InboundChan():
		raw := msg.Context.Raw
		if raw["whatsapp_sender_jid"] != lidJID.String() {
			t.Errorf("whatsapp_sender_jid = %q, want %q", raw["whatsapp_sender_jid"], lidJID.String())
		}
		if raw["whatsapp_linked_phone_jid"] != phoneJID {
			t.Errorf("whatsapp_linked_phone_jid = %q, want %q", raw["whatsapp_linked_phone_jid"], phoneJID)
		}
		if raw["whatsapp_linked_phone_number"] != "+33695651381" {
			t.Errorf("whatsapp_linked_phone_number = %q, want +33695651381", raw["whatsapp_linked_phone_number"])
		}
		if raw["whatsapp_linked_phone_source"] != "sender_alt" {
			t.Errorf("whatsapp_linked_phone_source = %q, want sender_alt", raw["whatsapp_linked_phone_source"])
		}
		if msg.Context.SenderID != lidJID.String() {
			t.Errorf("SenderID = %q, want raw LID %q", msg.Context.SenderID, lidJID.String())
		}
	default:
		t.Fatal("expected inbound message")
	}
}

func TestHandleIncoming_LinkedPhone_LIDLookup_AddsMetadata(t *testing.T) {
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
			ID:       "mid-lp2",
			PushName: "Alice",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("hello"),
		},
	}

	ch.handleIncoming(evt)

	select {
	case msg := <-msgBus.InboundChan():
		raw := msg.Context.Raw
		if raw["whatsapp_sender_jid"] != lidJID.String() {
			t.Errorf("whatsapp_sender_jid = %q, want %q", raw["whatsapp_sender_jid"], lidJID.String())
		}
		if raw["whatsapp_linked_phone_jid"] != phoneJID {
			t.Errorf("whatsapp_linked_phone_jid = %q, want %q", raw["whatsapp_linked_phone_jid"], phoneJID)
		}
		if raw["whatsapp_linked_phone_number"] != "+33695651381" {
			t.Errorf("whatsapp_linked_phone_number = %q, want +33695651381", raw["whatsapp_linked_phone_number"])
		}
		if raw["whatsapp_linked_phone_source"] != "lid_lookup" {
			t.Errorf("whatsapp_linked_phone_source = %q, want lid_lookup", raw["whatsapp_linked_phone_source"])
		}
		if msg.Context.SenderID != lidJID.String() {
			t.Errorf("SenderID = %q, want raw LID %q", msg.Context.SenderID, lidJID.String())
		}
	default:
		t.Fatal("expected inbound message")
	}
}

func TestHandleIncoming_DirectMatch_NoLinkedMetadata(t *testing.T) {
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
		Message: &waE2E.Message{
			Conversation: proto.String("hello"),
		},
	}

	ch.handleIncoming(evt)

	select {
	case msg := <-msgBus.InboundChan():
		raw := msg.Context.Raw
		if _, ok := raw["whatsapp_linked_phone_number"]; ok {
			t.Errorf("expected no whatsapp_linked_phone_number for direct match, got %q", raw["whatsapp_linked_phone_number"])
		}
		if _, ok := raw["whatsapp_sender_jid"]; ok {
			t.Errorf("expected no whatsapp_sender_jid for direct match")
		}
	default:
		t.Fatal("expected inbound message")
	}
}
