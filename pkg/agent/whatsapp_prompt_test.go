package agent

import (
	"strings"
	"testing"
)

func TestBuildDynamicContext_WhatsAppLinkedPhone_IncludesPhoneLine(t *testing.T) {
	cb := &ContextBuilder{}
	raw := map[string]string{
		"whatsapp_linked_phone_number": "+33695651381",
		"whatsapp_linked_phone_source": "lid_lookup",
	}
	got := cb.buildDynamicContext("whatsapp", "chat123", "lid-abc@lid.whatsapp.net", "Alice", raw)
	want := "WhatsApp linked phone number for this conversation: +33695651381"
	if !strings.Contains(got, want) {
		t.Errorf("expected prompt to contain %q\nGot:\n%s", want, got)
	}
}

func TestBuildDynamicContext_WhatsAppLinkedPhone_SenderAlt_IncludesPhoneLine(t *testing.T) {
	cb := &ContextBuilder{}
	raw := map[string]string{
		"whatsapp_linked_phone_number": "+14155550100",
		"whatsapp_linked_phone_source": "sender_alt",
	}
	got := cb.buildDynamicContext("whatsapp", "chat456", "lid-xyz@lid.whatsapp.net", "Bob", raw)
	if !strings.Contains(got, "WhatsApp linked phone number for this conversation: +14155550100") {
		t.Errorf("expected linked phone line in prompt\nGot:\n%s", got)
	}
	// The phrasing must NOT claim it definitively IS the user's phone number
	if strings.Contains(got, "user's phone number") {
		t.Errorf("prompt must not claim it is definitively the user's phone number\nGot:\n%s", got)
	}
}

func TestBuildDynamicContext_NoLinkedPhone_NoPhoneLine(t *testing.T) {
	cb := &ContextBuilder{}
	got := cb.buildDynamicContext("whatsapp", "chat789", "33695651381@s.whatsapp.net", "Alice", nil)
	if strings.Contains(got, "WhatsApp linked phone number") {
		t.Errorf("expected no linked phone line when raw metadata is nil\nGot:\n%s", got)
	}
}

func TestBuildDynamicContext_EmptyLinkedPhone_NoPhoneLine(t *testing.T) {
	cb := &ContextBuilder{}
	raw := map[string]string{
		"message_id": "abc123",
		// no whatsapp_linked_phone_number key
	}
	got := cb.buildDynamicContext("whatsapp", "chat000", "lid@lid.whatsapp.net", "", raw)
	if strings.Contains(got, "WhatsApp linked phone number") {
		t.Errorf("expected no linked phone line when key absent\nGot:\n%s", got)
	}
}

func TestBuildDynamicContext_LinkedPhone_NearCurrentSender(t *testing.T) {
	cb := &ContextBuilder{}
	raw := map[string]string{
		"whatsapp_linked_phone_number": "+33695651381",
	}
	got := cb.buildDynamicContext("whatsapp", "chat1", "lid@lid.whatsapp.net", "Alice", raw)

	senderIdx := strings.Index(got, "## Current Sender")
	phoneIdx := strings.Index(got, "WhatsApp linked phone number for this conversation")
	if senderIdx < 0 {
		t.Fatal("expected ## Current Sender section")
	}
	if phoneIdx < 0 {
		t.Fatal("expected linked phone line")
	}
	// Phone line must appear after the Current Sender header
	if phoneIdx < senderIdx {
		t.Errorf("phone line (at %d) should appear after Current Sender (at %d)", phoneIdx, senderIdx)
	}
	// No new section header should appear between the end of "## Current Sender\n"
	// and the phone line.
	afterHeader := senderIdx + len("## Current Sender")
	between := got[afterHeader:phoneIdx]
	if strings.Contains(between, "##") {
		t.Errorf(
			"expected phone line within Current Sender section, but found another section header between them: %q",
			between,
		)
	}
}
