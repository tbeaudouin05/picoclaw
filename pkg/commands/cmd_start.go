package commands

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

const defaultStartMessage = "Hello! I am PicoClaw 🦞"

func startCommand() Definition {
	return Definition{
		Name:        "start",
		Description: "Start the bot",
		Usage:       "/start",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			return req.Reply(resolveStartMessage(req.Channel, rt))
		},
	}
}

// resolveStartMessage returns the configured start message for the channel,
// falling back to defaultStartMessage when absent or blank.
func resolveStartMessage(channel string, rt *Runtime) string {
	if rt == nil || rt.Config == nil || channel == "" {
		return defaultStartMessage
	}
	bc := rt.Config.Channels[channel]
	if bc == nil {
		return defaultStartMessage
	}
	decoded, err := bc.GetDecoded()
	if err != nil || decoded == nil {
		return defaultStartMessage
	}
	settings, ok := decoded.(*config.TelegramSettings)
	if !ok || settings == nil || strings.TrimSpace(settings.StartMessage) == "" {
		return defaultStartMessage
	}
	return settings.StartMessage
}
