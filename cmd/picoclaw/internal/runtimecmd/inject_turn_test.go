package runtimecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestNewRuntimeCommand_IncludesInjectTurnSurfaces(t *testing.T) {
	cmd := NewRuntimeCommand()
	if cmd == nil {
		t.Fatal("NewRuntimeCommand returned nil")
	}
	admin, _, err := cmd.Find([]string{"admin", "inject-turn"})
	if err != nil {
		t.Fatalf("find admin inject-turn: %v", err)
	}
	if admin == nil || admin.Name() != "inject-turn" {
		t.Fatalf("admin inject-turn command missing")
	}
	customer, _, err := cmd.Find([]string{"customer", "inject-turn"})
	if err != nil {
		t.Fatalf("find customer inject-turn: %v", err)
	}
	if customer == nil || customer.Name() != "inject-turn" {
		t.Fatalf("customer inject-turn command missing")
	}
}

func TestWriteInjectTurnResponse(t *testing.T) {
	var buf bytes.Buffer
	req := injectTurnRequest{Text: "hello", SessionKey: "sess-1", ChatID: "chat-1", SenderID: "sender-1", DisplayName: "Demo"}
	if err := writeInjectTurnResponse(&buf, "telegram", "hello back", req); err != nil {
		t.Fatalf("writeInjectTurnResponse: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v", got["status"])
	}
	if got["channel"] != "telegram" {
		t.Fatalf("channel = %v", got["channel"])
	}
	if got["response"] != "hello back" {
		t.Fatalf("response = %v", got["response"])
	}
	if got["session_key"] != "sess-1" {
		t.Fatalf("session_key = %v", got["session_key"])
	}
}

func TestInjectTurnCommand_DefaultsAndDispatch(t *testing.T) {
	oldRunner := injectTurnRunner
	defer func() { injectTurnRunner = oldRunner }()

	type call struct {
		channel string
		req     injectTurnRequest
	}
	var got call
	injectTurnRunner = func(_ context.Context, channel string, req injectTurnRequest) (string, error) {
		got = call{channel: channel, req: req}
		return "hello back", nil
	}

	cmd := NewRuntimeCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"admin", "inject-turn"})
	cmd.SetIn(bytes.NewBufferString(`{"text":"hello"}`))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.channel != "telegram" {
		t.Fatalf("channel = %q", got.channel)
	}
	if got.req.SessionKey != "runtime:admin:telegram" {
		t.Fatalf("session key = %q", got.req.SessionKey)
	}
	if got.req.ChatID != "runtime-admin" {
		t.Fatalf("chat id = %q", got.req.ChatID)
	}
	if got.req.SenderID != "runtime-admin" {
		t.Fatalf("sender id = %q", got.req.SenderID)
	}
	var resp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["response"] != "hello back" {
		t.Fatalf("response = %v", resp["response"])
	}
}

func TestInjectTurnCommand_RequiresText(t *testing.T) {
	oldRunner := injectTurnRunner
	defer func() { injectTurnRunner = oldRunner }()
	injectTurnRunner = func(_ context.Context, channel string, req injectTurnRequest) (string, error) {
		t.Fatalf("runner should not be called, got channel=%q req=%+v", channel, req)
		return "", nil
	}

	cmd := NewRuntimeCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"customer", "inject-turn"})
	cmd.SetIn(bytes.NewBufferString(`{"text":"   "}`))
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error")
	}
	var resp map[string]any
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp["status"] != "error" {
		t.Fatalf("status = %v", resp["status"])
	}
}
