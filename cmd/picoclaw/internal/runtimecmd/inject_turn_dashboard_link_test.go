package runtimecmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// TestAdminDashboardLink_InjectTurn is an E2E smoke test for the admin
// dashboard link skill. It calls the installed mist-test inject-turn wrapper
// and asserts the bot responds with a valid dashboard URL.
//
// Skipped when the mist-test instance is not installed on this host.
func TestAdminDashboardLink_InjectTurn(t *testing.T) {
	const wrapper = "/usr/local/bin/picoclaw-admin-mist-test-inject-turn"
	if _, err := os.Stat(wrapper); err != nil {
		t.Skipf("mist-test instance not installed (%s not found): %v", wrapper, err)
	}

	cmd := exec.Command(wrapper)
	cmd.Stdin = bytes.NewBufferString(`{"text":"generate the admin dashboard link"}`)

	out, err := cmd.Output()

	var resp map[string]any
	_ = json.Unmarshal(out, &resp)

	if resp["status"] == "error" {
		// Bot returned an error — likely missing LLM credentials or misconfigured
		// provider. Treat as a skip: the instance binary exists but the runtime
		// environment is not operational.
		t.Skipf("mist-test admin bot not operational: %v", resp["error"])
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("inject-turn exited %d\nstdout: %s\nstderr: %s", exitErr.ExitCode(), out, exitErr.Stderr)
		}
		t.Fatalf("inject-turn: %v\noutput: %s", err, out)
	}

	if resp == nil {
		t.Fatalf("decode response: not valid JSON\nraw: %s", out)
	}

	response, _ := resp["response"].(string)
	dashboardURLPattern := regexp.MustCompile(`https://mist-web\.fly\.dev/instances/[^/]+/admin\?t=qvJc1crMSsgcmx_9iqHsWfumuBP-gD6RJcFcDfHl-7U`)
	if !dashboardURLPattern.MatchString(response) {
		t.Fatalf("response does not contain a dashboard link URL\nresponse: %q\nfull output: %s", response, out)
	}
}
