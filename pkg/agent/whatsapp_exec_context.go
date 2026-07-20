// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// e164Pattern matches an E.164 phone number: a leading "+" followed by a
// non-zero leading digit and up to 14 additional digits (max 15 digits total).
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

// whatsAppSenderE164FromInbound returns the authenticated native WhatsApp
// sender phone number in E.164 form for the given inbound context, or "" when
// the turn does not originate from an authenticated native WhatsApp sender.
//
// It consumes ONLY bus.RawKeyWhatsAppAuthenticatedSenderE164 — the native-
// WhatsApp-channel-owned trusted marker (see pkg/channels/whatsapp_native),
// which is populated for both direct phone-JID senders and LID senders whose
// phone was resolved through authenticated native WhatsApp metadata. It never
// consults tool arguments, prompt/model context, the channel string, or any
// other Raw field, and it fails closed (returns "") when the marker is absent.
// The value must additionally pass E.164 validation; malformed values yield "".
func whatsAppSenderE164FromInbound(inbound *bus.InboundContext) string {
	if inbound == nil || len(inbound.Raw) == 0 {
		return ""
	}
	phone := strings.TrimSpace(inbound.Raw[bus.RawKeyWhatsAppAuthenticatedSenderE164])
	if !e164Pattern.MatchString(phone) {
		return ""
	}
	return phone
}
