package integrationtools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

type WhatsAppSendTool struct {
	sendCallback SendCallbackWithContext
}

func NewWhatsAppSendTool() *WhatsAppSendTool {
	return &WhatsAppSendTool{}
}

func (t *WhatsAppSendTool) Name() string {
	return "whatsapp_send"
}

func (t *WhatsAppSendTool) Description() string {
	return "Send a WhatsApp message to a specific JID (phone number or group ID)."
}

func (t *WhatsAppSendTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"jid": map[string]any{
				"type":        "string",
				"description": "WhatsApp JID to send the message to (e.g. 1234567890@s.whatsapp.net or groupid@g.us).",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Message text to send.",
			},
		},
		"required": []string{"jid", "content"},
	}
}

func (t *WhatsAppSendTool) SetSendCallback(cb SendCallbackWithContext) {
	t.sendCallback = cb
}

func (t *WhatsAppSendTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t.sendCallback == nil {
		return ErrorResult("WhatsApp send not configured")
	}
	jid, _ := args["jid"].(string)
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ErrorResult("jid is required")
	}
	content, _ := args["content"].(string)
	content = strings.TrimSpace(content)
	if content == "" {
		return ErrorResult("content is required")
	}

	if err := t.sendCallback(ctx, "whatsapp", jid, content, "", []bus.MediaPart(nil)); err != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("sending WhatsApp message: %v", err),
			IsError: true,
			Err:     err,
		}
	}

	return SilentResult(fmt.Sprintf("WhatsApp message sent to %s", jid))
}
