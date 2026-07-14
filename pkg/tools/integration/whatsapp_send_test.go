package integrationtools

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestWhatsAppSendTool_Name(t *testing.T) {
	tool := NewWhatsAppSendTool()
	if tool.Name() != "whatsapp_send" {
		t.Errorf("expected name 'whatsapp_send', got %q", tool.Name())
	}
}

func TestWhatsAppSendTool_Parameters(t *testing.T) {
	tool := NewWhatsAppSendTool()
	params := tool.Parameters()

	typ, ok := params["type"].(string)
	if !ok || typ != "object" {
		t.Error("expected type 'object'")
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	if _, exists := props["jid"]; !exists {
		t.Error("expected 'jid' property")
	}
	if _, exists := props["content"]; !exists {
		t.Error("expected 'content' property")
	}
	required, ok := params["required"].([]string)
	if !ok || len(required) != 2 {
		t.Fatal("expected exactly two required fields")
	}
	has := func(s string) bool {
		for _, r := range required {
			if r == s {
				return true
			}
		}
		return false
	}
	if !has("jid") || !has("content") {
		t.Error("expected jid and content in required")
	}
}

func TestWhatsAppSendTool_Execute_Success(t *testing.T) {
	tool := NewWhatsAppSendTool()

	var gotChannel, gotChatID, gotContent string
	tool.SetSendCallback(func(
		ctx context.Context,
		channel, chatID, content, replyToMessageID string,
		mediaParts []bus.MediaPart,
	) error {
		gotChannel = channel
		gotChatID = chatID
		gotContent = content
		return nil
	})

	ctx := context.Background()
	result := tool.Execute(ctx, map[string]any{
		"jid":     "15551234567@s.whatsapp.net",
		"content": "Hello from test",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Error("expected Silent=true")
	}
	if gotChannel != "whatsapp" {
		t.Errorf("expected channel 'whatsapp', got %q", gotChannel)
	}
	if gotChatID != "15551234567@s.whatsapp.net" {
		t.Errorf("expected jid passthrough, got %q", gotChatID)
	}
	if gotContent != "Hello from test" {
		t.Errorf("expected content passthrough, got %q", gotContent)
	}
}

func TestWhatsAppSendTool_Execute_FullJIDPassthrough(t *testing.T) {
	tool := NewWhatsAppSendTool()

	const jid = "123456789-1609459200@g.us"
	var gotChatID string
	tool.SetSendCallback(func(
		ctx context.Context,
		channel, chatID, content, replyToMessageID string,
		mediaParts []bus.MediaPart,
	) error {
		gotChatID = chatID
		return nil
	})

	result := tool.Execute(context.Background(), map[string]any{
		"jid":     jid,
		"content": "group message",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if gotChatID != jid {
		t.Errorf("JID not passed through: got %q, want %q", gotChatID, jid)
	}
}

func TestWhatsAppSendTool_Execute_MissingJID(t *testing.T) {
	tool := NewWhatsAppSendTool()
	tool.SetSendCallback(
		func(ctx context.Context, channel, chatID, content, replyToMessageID string, mediaParts []bus.MediaPart) error {
			return nil
		},
	)

	result := tool.Execute(context.Background(), map[string]any{
		"content": "Hello",
	})
	if !result.IsError {
		t.Fatal("expected IsError=true for missing jid")
	}
	if result.ForLLM != "jid is required" {
		t.Errorf("unexpected error message: %q", result.ForLLM)
	}
}

func TestWhatsAppSendTool_Execute_MissingContent(t *testing.T) {
	tool := NewWhatsAppSendTool()
	tool.SetSendCallback(
		func(ctx context.Context, channel, chatID, content, replyToMessageID string, mediaParts []bus.MediaPart) error {
			return nil
		},
	)

	result := tool.Execute(context.Background(), map[string]any{
		"jid": "15551234567@s.whatsapp.net",
	})
	if !result.IsError {
		t.Fatal("expected IsError=true for missing content")
	}
	if result.ForLLM != "content is required" {
		t.Errorf("unexpected error message: %q", result.ForLLM)
	}
}

func TestWhatsAppSendTool_Execute_MissingCallback(t *testing.T) {
	tool := NewWhatsAppSendTool()
	// No SetSendCallback

	result := tool.Execute(context.Background(), map[string]any{
		"jid":     "15551234567@s.whatsapp.net",
		"content": "Hello",
	})
	if !result.IsError {
		t.Fatal("expected IsError=true when callback not configured")
	}
	if result.ForLLM != "WhatsApp send not configured" {
		t.Errorf("unexpected error message: %q", result.ForLLM)
	}
}

func TestWhatsAppSendTool_Execute_SendFailure(t *testing.T) {
	tool := NewWhatsAppSendTool()
	sendErr := errors.New("connection refused")
	tool.SetSendCallback(
		func(ctx context.Context, channel, chatID, content, replyToMessageID string, mediaParts []bus.MediaPart) error {
			return sendErr
		},
	)

	result := tool.Execute(context.Background(), map[string]any{
		"jid":     "15551234567@s.whatsapp.net",
		"content": "Hello",
	})
	if !result.IsError {
		t.Fatal("expected IsError=true on send failure")
	}
	if result.ForLLM != "sending WhatsApp message: connection refused" {
		t.Errorf("unexpected error message: %q", result.ForLLM)
	}
	if result.Err != sendErr {
		t.Errorf("expected Err to be sendErr, got %v", result.Err)
	}
}
