// Package constants provides shared constants across the codebase.
package constants

// Environment variables that PicoClaw injects into the child processes it
// spawns for exec-style tools. Consumers (shell scripts, subprocesses) read
// these; PicoClaw itself only sets them. All picoclaw-specific keys use the
// PICOCLAW_ prefix.
const (
	// EnvWhatsAppSenderE164 carries the authenticated native WhatsApp sender's
	// phone number, in E.164 form (e.g. "+33695651381"), into the environment
	// of exec-tool child processes.
	//
	// It is only set when the current turn originates from an authenticated
	// native WhatsApp inbound message whose sender identity was resolved from
	// platform metadata. It is never derived from agent/model tool arguments or
	// command input, and is absent for all other sources.
	EnvWhatsAppSenderE164 = "PICOCLAW_WHATSAPP_SENDER_E164"
)
