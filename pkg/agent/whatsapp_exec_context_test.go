// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// TestWhatsAppSenderE164FromInbound covers derivation of the authenticated
// native WhatsApp sender identity from inbound context metadata. It verifies the
// value is read solely from the native-channel-owned trusted marker
// (bus.RawKeyWhatsAppAuthenticatedSenderE164), and that non-native, absent,
// spoofed, or malformed sources fail closed to "".
func TestWhatsAppSenderE164FromInbound(t *testing.T) {
	const marker = bus.RawKeyWhatsAppAuthenticatedSenderE164

	tests := []struct {
		name    string
		inbound *bus.InboundContext
		want    string
	}{
		{
			name: "direct phone-JID sender (marker present)",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: "+33695651381"},
			},
			want: "+33695651381",
		},
		{
			name: "LID-resolved sender (marker present)",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw: map[string]string{
					marker:                         "+14155550100",
					"whatsapp_linked_phone_number": "+14155550100",
					"whatsapp_linked_phone_source": "lid_lookup",
				},
			},
			want: "+14155550100",
		},
		{
			name: "trims surrounding whitespace",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: "  +14155550100  "},
			},
			want: "+14155550100",
		},
		{
			name:    "nil inbound",
			inbound: nil,
			want:    "",
		},
		{
			name: "no raw metadata",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
			},
			want: "",
		},
		{
			name: "marker absent (non-native source)",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{"user_name": "Alice"},
			},
			want: "",
		},
		{
			name: "linked-phone key alone is not trusted (marker absent)",
			inbound: &bus.InboundContext{
				// Only the user-facing prompt key is present, not the trusted
				// marker — e.g. a source that forged the prompt field. Derivation
				// must ignore it and fail closed.
				Channel: "whatsapp",
				Raw:     map[string]string{"whatsapp_linked_phone_number": "+33695651381"},
			},
			want: "",
		},
		{
			name: "empty marker value",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: ""},
			},
			want: "",
		},
		{
			name: "invalid e164 missing plus",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: "33695651381"},
			},
			want: "",
		},
		{
			name: "invalid e164 leading zero",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: "+0333"},
			},
			want: "",
		},
		{
			name: "invalid e164 non-digit",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: "+3369x651381"},
			},
			want: "",
		},
		{
			name: "invalid e164 too long",
			inbound: &bus.InboundContext{
				Channel: "whatsapp",
				Raw:     map[string]string{marker: "+1234567890123456"},
			},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := whatsAppSenderE164FromInbound(tc.inbound)
			if got != tc.want {
				t.Fatalf("whatsAppSenderE164FromInbound() = %q, want %q", got, tc.want)
			}
		})
	}
}
