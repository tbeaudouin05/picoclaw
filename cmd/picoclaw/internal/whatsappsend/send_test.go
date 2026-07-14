package whatsappsend

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     SendRequest
		wantErr bool
	}{
		{
			name: "valid whatsapp_native",
			req:  SendRequest{Channel: config.ChannelWhatsAppNative, To: "12025550100", Text: "hi"},
		},
		{
			name: "valid whatsapp",
			req:  SendRequest{Channel: config.ChannelWhatsApp, To: "12025550100@s.whatsapp.net", Text: "hi"},
		},
		{
			name:    "missing channel",
			req:     SendRequest{To: "12025550100", Text: "hi"},
			wantErr: true,
		},
		{
			name:    "unsupported channel",
			req:     SendRequest{Channel: "telegram", To: "123", Text: "hi"},
			wantErr: true,
		},
		{
			name:    "missing recipient",
			req:     SendRequest{Channel: config.ChannelWhatsAppNative, Text: "hi"},
			wantErr: true,
		},
		{
			name:    "blank recipient",
			req:     SendRequest{Channel: config.ChannelWhatsAppNative, To: "   ", Text: "hi"},
			wantErr: true,
		},
		{
			name:    "missing text",
			req:     SendRequest{Channel: config.ChannelWhatsAppNative, To: "123"},
			wantErr: true,
		},
		{
			name:    "blank text",
			req:     SendRequest{Channel: config.ChannelWhatsAppNative, To: "123", Text: "   "},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequest(tt.req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, errInvalidRequest) {
					t.Fatalf("expected errInvalidRequest, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRequestErrorOmitsBody(t *testing.T) {
	secret := "super-secret-booking-code-9931"
	err := validateRequest(SendRequest{Channel: "telegram", To: "123", Text: secret})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error must not contain the message body: %v", err)
	}
}

// fakeChannel implements textChannel and records the lifecycle for assertions.
type fakeChannel struct {
	mu        sync.Mutex
	startErr  error
	sendErr   error
	started   bool
	stopped   bool
	sentMsg   *bus.OutboundMessage
	sendCalls int
}

func (f *fakeChannel) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return f.startErr
}

func (f *fakeChannel) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *fakeChannel) Send(_ context.Context, msg bus.OutboundMessage) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls++
	m := msg
	f.sentMsg = &m
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return []string{"msg-1"}, nil
}

func newResolver(ch textChannel, cleanup func(), err error) (channelResolver, *bool) {
	cleaned := false
	wrapped := func() {
		cleaned = true
		if cleanup != nil {
			cleanup()
		}
	}
	return func(context.Context, *config.Config, SendRequest) (textChannel, func(), error) {
		if err != nil {
			return nil, nil, err
		}
		return ch, wrapped, nil
	}, &cleaned
}

func TestSendHappyPath(t *testing.T) {
	fake := &fakeChannel{}
	resolve, cleaned := newResolver(fake, nil, nil)

	req := SendRequest{Channel: config.ChannelWhatsAppNative, To: "12025550100", Text: "your booking is confirmed"}
	if err := Send(context.Background(), &config.Config{}, req, resolve); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.started {
		t.Error("channel was not started")
	}
	if !fake.stopped {
		t.Error("channel was not stopped")
	}
	if fake.sendCalls != 1 {
		t.Errorf("expected exactly 1 send, got %d", fake.sendCalls)
	}
	if fake.sentMsg == nil {
		t.Fatal("no message captured")
	}
	if fake.sentMsg.ChatID != req.To {
		t.Errorf("ChatID = %q, want %q", fake.sentMsg.ChatID, req.To)
	}
	if fake.sentMsg.Channel != req.Channel {
		t.Errorf("Channel = %q, want %q", fake.sentMsg.Channel, req.Channel)
	}
	if fake.sentMsg.Content != req.Text {
		t.Errorf("Content = %q, want %q", fake.sentMsg.Content, req.Text)
	}
	if !*cleaned {
		t.Error("cleanup was not invoked")
	}
}

func TestSendValidationShortCircuits(t *testing.T) {
	fake := &fakeChannel{}
	resolve, _ := newResolver(fake, nil, nil)

	err := Send(context.Background(), &config.Config{}, SendRequest{Channel: "irc", To: "x", Text: "y"}, resolve)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if fake.started || fake.sendCalls != 0 {
		t.Error("channel must not be touched when validation fails")
	}
}

func TestSendStartErrorIsSurfaced(t *testing.T) {
	fake := &fakeChannel{startErr: errors.New("not yet paired")}
	resolve, _ := newResolver(fake, nil, nil)

	err := Send(
		context.Background(),
		&config.Config{},
		SendRequest{Channel: config.ChannelWhatsAppNative, To: "123", Text: "hi"},
		resolve,
	)
	if err == nil {
		t.Fatal("expected start error")
	}
	if fake.sendCalls != 0 {
		t.Error("send must not run when start fails")
	}
}

func TestSendDeliveryErrorIsSurfacedWithoutBody(t *testing.T) {
	fake := &fakeChannel{sendErr: errors.New("server returned error 463")}
	resolve, _ := newResolver(fake, nil, nil)

	body := "confidential-reminder-7788"
	err := Send(
		context.Background(),
		&config.Config{},
		SendRequest{Channel: config.ChannelWhatsAppNative, To: "123", Text: body},
		resolve,
	)
	if err == nil {
		t.Fatal("expected send error")
	}
	if strings.Contains(err.Error(), body) {
		t.Fatalf("error must not contain message body: %v", err)
	}
	if !fake.stopped {
		t.Error("channel should still be stopped after a send failure")
	}
}

func TestSendResolverErrorIsSurfaced(t *testing.T) {
	resolve, _ := newResolver(nil, nil, errors.New("channel \"whatsapp_native\" is not configured"))

	err := Send(
		context.Background(),
		&config.Config{},
		SendRequest{Channel: config.ChannelWhatsAppNative, To: "123", Text: "hi"},
		resolve,
	)
	if err == nil {
		t.Fatal("expected resolver error")
	}
}

func TestSendNilConfig(t *testing.T) {
	fake := &fakeChannel{}
	resolve, _ := newResolver(fake, nil, nil)
	err := Send(
		context.Background(),
		nil,
		SendRequest{Channel: config.ChannelWhatsAppNative, To: "123", Text: "hi"},
		resolve,
	)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}
