package config

import "testing"

func isolateCredentialEnvForTest(t *testing.T) {
	t.Helper()
	t.Setenv("PICOCLAW_CHANNELS_TELEGRAM_TOKEN", "")
	t.Setenv("PICOCLAW_CHANNELS_SLACK_BOT_TOKEN", "")
	t.Setenv("PICOCLAW_CHANNELS_SLACK_APP_TOKEN", "")
}
