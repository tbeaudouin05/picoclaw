package whatsappsend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// withStubs swaps the command seams for the duration of a test.
func withStubs(
	t *testing.T,
	loader func() (*config.Config, error),
	runner func(context.Context, *config.Config, SendRequest) error,
) {
	t.Helper()
	origLoader := configLoader
	origRunner := sendRunner
	configLoader = loader
	sendRunner = runner
	t.Cleanup(func() {
		configLoader = origLoader
		sendRunner = origRunner
	})
}

func runCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewWhatsAppSendCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCommandHappyPath(t *testing.T) {
	var gotReq SendRequest
	loaded := false
	withStubs(t,
		func() (*config.Config, error) { loaded = true; return &config.Config{}, nil },
		func(_ context.Context, _ *config.Config, req SendRequest) error { gotReq = req; return nil },
	)

	out, err := runCommand(t,
		"--channel", config.ChannelWhatsAppNative,
		"--to", "12025550100",
		"--text", "your booking is confirmed",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !loaded {
		t.Error("config loader was not invoked")
	}
	if gotReq.Channel != config.ChannelWhatsAppNative || gotReq.To != "12025550100" ||
		gotReq.Text != "your booking is confirmed" {
		t.Errorf("unexpected request passed to runner: %+v", gotReq)
	}
	if !strings.Contains(out, "sent:") {
		t.Errorf("expected success line, got %q", out)
	}
}

func TestCommandTrimsWhitespaceFlags(t *testing.T) {
	var gotReq SendRequest
	withStubs(t,
		func() (*config.Config, error) { return &config.Config{}, nil },
		func(_ context.Context, _ *config.Config, req SendRequest) error { gotReq = req; return nil },
	)

	if _, err := runCommand(t, "--channel", "  whatsapp_native ", "--to", "  123 ", "--text", "hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReq.Channel != "whatsapp_native" || gotReq.To != "123" {
		t.Errorf("flags were not trimmed: %+v", gotReq)
	}
}

func TestCommandReadsTextFile(t *testing.T) {
	var gotReq SendRequest
	withStubs(t,
		func() (*config.Config, error) { return &config.Config{}, nil },
		func(_ context.Context, _ *config.Config, req SendRequest) error { gotReq = req; return nil },
	)

	file := t.TempDir() + "/message.txt"
	body := "your booking is confirmed from file"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, "--to", "123", "--text-file", file); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReq.Text != body {
		t.Fatalf("Text = %q, want %q", gotReq.Text, body)
	}
}

func TestCommandReadsTextFromStdin(t *testing.T) {
	var gotReq SendRequest
	withStubs(t,
		func() (*config.Config, error) { return &config.Config{}, nil },
		func(_ context.Context, _ *config.Config, req SendRequest) error { gotReq = req; return nil },
	)

	cmd := NewWhatsAppSendCommand()
	cmd.SetIn(strings.NewReader("stdin reminder body"))
	cmd.SetArgs([]string{"--to", "123", "--text-file", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReq.Text != "stdin reminder body" {
		t.Fatalf("Text = %q", gotReq.Text)
	}
}

func TestCommandRejectsTextAndTextFileBeforeConfigLoad(t *testing.T) {
	loaded := false
	withStubs(t,
		func() (*config.Config, error) { loaded = true; return &config.Config{}, nil },
		func(context.Context, *config.Config, SendRequest) error { t.Fatal("runner should not run"); return nil },
	)

	file := t.TempDir() + "/message.txt"
	if err := os.WriteFile(file, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCommand(t, "--to", "123", "--text", "body", "--text-file", file)
	if err == nil {
		t.Fatal("expected mutually exclusive text flags error")
	}
	if loaded {
		t.Error("config must not be loaded when text flags are invalid")
	}
}

func TestCommandUnsupportedChannelFailsBeforeConfigLoad(t *testing.T) {
	loaded := false
	ran := false
	withStubs(t,
		func() (*config.Config, error) { loaded = true; return &config.Config{}, nil },
		func(context.Context, *config.Config, SendRequest) error { ran = true; return nil },
	)

	_, err := runCommand(t, "--channel", "telegram", "--to", "123", "--text", "hi")
	if err == nil {
		t.Fatal("expected error for unsupported channel")
	}
	if loaded {
		t.Error("config must not be loaded when guard rails reject the request")
	}
	if ran {
		t.Error("send runner must not run when guard rails reject the request")
	}
}

func TestCommandMissingRecipientFails(t *testing.T) {
	withStubs(t,
		func() (*config.Config, error) { t.Fatal("config should not load"); return nil, nil },
		func(context.Context, *config.Config, SendRequest) error { t.Fatal("runner should not run"); return nil },
	)

	if _, err := runCommand(t, "--text", "hi"); err == nil {
		t.Fatal("expected error for missing recipient")
	}
}

func TestCommandConfigLoadErrorIsSurfaced(t *testing.T) {
	withStubs(t,
		func() (*config.Config, error) { return nil, errors.New("boom") },
		func(context.Context, *config.Config, SendRequest) error { t.Fatal("runner should not run"); return nil },
	)

	_, err := runCommand(t, "--channel", "whatsapp", "--to", "123", "--text", "hi")
	if err == nil {
		t.Fatal("expected config load error")
	}
}

func TestCommandDeliveryErrorIsSurfaced(t *testing.T) {
	withStubs(t,
		func() (*config.Config, error) { return &config.Config{}, nil },
		func(context.Context, *config.Config, SendRequest) error { return errors.New("not paired") },
	)

	_, err := runCommand(t, "--channel", "whatsapp", "--to", "123", "--text", "hi")
	if err == nil {
		t.Fatal("expected delivery error")
	}
}

func TestCommandDefaultChannelIsNative(t *testing.T) {
	var gotReq SendRequest
	withStubs(t,
		func() (*config.Config, error) { return &config.Config{}, nil },
		func(_ context.Context, _ *config.Config, req SendRequest) error { gotReq = req; return nil },
	)

	if _, err := runCommand(t, "--to", "123", "--text", "hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReq.Channel != config.ChannelWhatsAppNative {
		t.Errorf("default channel = %q, want %q", gotReq.Channel, config.ChannelWhatsAppNative)
	}
}
