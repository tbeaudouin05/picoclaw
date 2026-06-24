// Package whatsappsend implements a narrow, non-agent-turn entrypoint for
// delivering a single WhatsApp text message through PicoClaw's existing channel
// abstractions. It is intended to be called by an external deterministic
// dispatcher (e.g. Mist booking reminders) without spinning up an LLM provider
// or running an agent loop.
//
// Scope is intentionally tight: only the WhatsApp send paths are reachable, no
// arbitrary channels, tools, or shell execution are exposed, and message bodies
// are never written to logs or error strings.
package whatsappsend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// allowedChannels enumerates the only channel names this entrypoint may target.
// Restricting to the WhatsApp send paths keeps the dispatcher primitive from
// being repurposed to drive arbitrary channels.
var allowedChannels = map[string]struct{}{
	config.ChannelWhatsApp:       {},
	config.ChannelWhatsAppNative: {},
}

// SendRequest describes a single outbound WhatsApp text message.
type SendRequest struct {
	// Channel is the configured channel name to send through. It must be
	// "whatsapp" or "whatsapp_native".
	Channel string
	// To is the recipient / chat identifier (phone number or WhatsApp JID).
	To string
	// Text is the message body. It is never logged.
	Text string
}

// errInvalidRequest wraps guard-rail validation failures so callers can
// distinguish bad input from delivery failures if they wish.
var errInvalidRequest = errors.New("invalid whatsapp send request")

// validateRequest enforces the narrow guard rails: a known channel and a
// non-empty recipient and body. Returned errors never include the message body.
func validateRequest(req SendRequest) error {
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		return fmt.Errorf("%w: channel is required", errInvalidRequest)
	}
	if _, ok := allowedChannels[channel]; !ok {
		return fmt.Errorf(
			"%w: unsupported channel %q (only %q and %q are allowed)",
			errInvalidRequest, channel, config.ChannelWhatsApp, config.ChannelWhatsAppNative,
		)
	}
	if strings.TrimSpace(req.To) == "" {
		return fmt.Errorf("%w: recipient is required", errInvalidRequest)
	}
	if strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("%w: message text is required", errInvalidRequest)
	}
	return nil
}

// textChannel is the minimal subset of channels.Channel required to deliver one
// text message. channels.Channel satisfies it; tests provide a fake so the send
// boundary can be exercised without real WhatsApp pairing.
type textChannel interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error)
}

// channelResolver returns the target channel for a validated request along with
// a cleanup func (which may be nil) to release any resources acquired while
// resolving. The production resolver builds a channel Manager from config;
// tests inject a fake.
type channelResolver func(ctx context.Context, cfg *config.Config, req SendRequest) (ch textChannel, cleanup func(), err error)

// managerChannelResolver resolves the target channel by constructing a channel
// Manager from the loaded config and looking up the requested channel by name.
//
// Building the Manager reuses the existing config decoding, channel factory, and
// native WhatsApp implementation. No LLM provider or agent loop is involved, and
// only the requested channel is started by Send (other configured channels are
// constructed but never connected).
func managerChannelResolver(_ context.Context, cfg *config.Config, req SendRequest) (textChannel, func(), error) {
	msgBus := bus.NewMessageBus()
	cleanup := func() { msgBus.Close() }

	// A media store is not required for plain text delivery; pass nil.
	mgr, err := channels.NewManager(cfg, msgBus, nil)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("create channel manager: %w", err)
	}

	ch, ok := mgr.GetChannel(req.Channel)
	if !ok {
		enabled := mgr.GetEnabledChannels()
		cleanup()
		return nil, nil, fmt.Errorf(
			"channel %q is not configured or not enabled (enabled channels: %s)",
			req.Channel, formatEnabled(enabled),
		)
	}
	return ch, cleanup, nil
}

func formatEnabled(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Send delivers a single WhatsApp text message through the resolved channel.
// It validates the request, starts the channel, sends synchronously, then stops
// the channel. The message body is never logged. Any failure is returned as an
// error so callers can exit non-zero.
func Send(ctx context.Context, cfg *config.Config, req SendRequest, resolve channelResolver) error {
	if err := validateRequest(req); err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("config is required")
	}
	if resolve == nil {
		resolve = managerChannelResolver
	}

	ch, cleanup, err := resolve(ctx, cfg, req)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	if err := ch.Start(ctx); err != nil {
		return fmt.Errorf("start channel %q: %w", req.Channel, err)
	}
	defer func() { _ = ch.Stop(ctx) }()

	msg := bus.NormalizeOutboundMessage(bus.OutboundMessage{
		Channel: req.Channel,
		ChatID:  req.To,
		Content: req.Text,
	})

	// Call the channel's Send directly: a deterministic one-shot dispatch does
	// not need the Manager's async worker/rate-limit/retry machinery, and the
	// channel returns classified errors (e.g. not paired, addressing) that are
	// safe to surface without exposing the message body.
	if _, err := ch.Send(ctx, msg); err != nil {
		return fmt.Errorf("send via channel %q failed: %w", req.Channel, err)
	}
	return nil
}
