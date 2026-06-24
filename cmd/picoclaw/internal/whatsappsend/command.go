package whatsappsend

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	picointernal "github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
)

// configLoader is the seam used to load config; overridable in tests.
var configLoader = picointernal.LoadConfig

// sendRunner is the seam used to perform delivery; overridable in tests so the
// command wiring can be exercised without a real WhatsApp connection.
var sendRunner = func(ctx context.Context, cfg *config.Config, req SendRequest) error {
	return Send(ctx, cfg, req, nil)
}

// NewWhatsAppSendCommand builds the `whatsapp-send` entrypoint: a deterministic,
// non-agent-turn primitive that delivers one WhatsApp text message through the
// configured native/channel send path. It loads the normal config, sends, and
// exits non-zero on failure. It never starts an LLM provider or agent loop.
func NewWhatsAppSendCommand() *cobra.Command {
	var (
		channel  string
		to       string
		text     string
		textFile string
	)

	cmd := &cobra.Command{
		Use:   "whatsapp-send",
		Short: "Send a single WhatsApp text message (no agent turn)",
		Long: "Deliver one WhatsApp text message through the configured channel " +
			"send path without running an LLM agent turn. Intended for external " +
			"deterministic dispatchers (e.g. booking reminders).",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		Hidden:        true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := resolveTextBody(cmd, text, strings.TrimSpace(textFile))
			if err != nil {
				return err
			}
			req := SendRequest{
				Channel: strings.TrimSpace(channel),
				To:      strings.TrimSpace(to),
				Text:    body,
			}
			// Validate guard rails before loading config so bad invocations fail
			// fast and never reach the channel layer.
			if err := validateRequest(req); err != nil {
				return err
			}

			cfg, err := configLoader()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			if err := sendRunner(cmd.Context(), cfg, req); err != nil {
				return err
			}

			// Deliberately minimal, body-free success line for the dispatcher.
			writeOK(cmd.OutOrStdout(), req)
			return nil
		},
	}

	cmd.Flags().StringVar(&channel, "channel", config.ChannelWhatsAppNative,
		"Channel to send through (whatsapp or whatsapp_native)")
	cmd.Flags().StringVar(&to, "to", "",
		"Recipient phone number or WhatsApp chat/JID")
	cmd.Flags().StringVar(&text, "text", "",
		"Message body to send (prefer --text-file=- or --text-file for dispatchers to avoid argv exposure)")
	cmd.Flags().StringVar(&textFile, "text-file", "",
		"Read message body from this file, or - for stdin; mutually exclusive with --text")

	return cmd
}

// writeOK prints a concise, body-free confirmation suitable for a dispatcher.
func writeOK(w io.Writer, req SendRequest) {
	fmt.Fprintf(w, "sent: channel=%s to=%s\n", req.Channel, req.To)
}

// resolveTextBody returns the outbound message body. Dispatchers should prefer
// --text-file (often "-" for stdin) so reminder text is not exposed in process
// arguments. Errors intentionally do not include body contents.
func resolveTextBody(cmd *cobra.Command, text, textFile string) (string, error) {
	if strings.TrimSpace(textFile) != "" && text != "" {
		return "", fmt.Errorf("%w: use only one of --text or --text-file", errInvalidRequest)
	}
	if strings.TrimSpace(textFile) == "" {
		return text, nil
	}
	var (
		data []byte
		err  error
	)
	if textFile == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(textFile)
	}
	if err != nil {
		return "", fmt.Errorf("read message body: %w", err)
	}
	return string(data), nil
}
