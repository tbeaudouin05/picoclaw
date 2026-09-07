package auth

import (
	"bytes"
	"errors"
	"os/exec"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLoginSubCommand(t *testing.T) {
	cmd := newLoginCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "Login via OAuth, token, or local CLI", cmd.Short)

	assert.True(t, cmd.HasFlags())

	assert.NotNil(t, cmd.Flags().Lookup("device-code"))
	assert.NotNil(t, cmd.Flags().Lookup("no-browser"))

	providerFlag := cmd.Flags().Lookup("provider")
	require.NotNil(t, providerFlag)
	assert.Contains(t, providerFlag.Usage, "antigravity-cli")

	val, found := providerFlag.Annotations[cobra.BashCompOneRequiredFlag]
	require.True(t, found)
	require.NotEmpty(t, val)
	assert.Equal(t, "true", val[0])
}

func TestAuthLoginAntigravityCLIUnavailable(t *testing.T) {
	originalLookPath := antigravityCLILookPath
	originalRun := antigravityCLIRun
	originalCheck := antigravityCLICheck
	t.Cleanup(func() {
		antigravityCLILookPath = originalLookPath
		antigravityCLIRun = originalRun
		antigravityCLICheck = originalCheck
	})
	antigravityCLILookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	antigravityCLIRun = func(string, ...string) error { t.Fatal("runner must not be called"); return nil }
	antigravityCLICheck = func(string) error { t.Fatal("check must not be called"); return nil }

	err := authLoginAntigravityCLI()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install agy")
}

func TestAuthLoginAntigravityCLIRunFailure(t *testing.T) {
	originalLookPath := antigravityCLILookPath
	originalRun := antigravityCLIRun
	originalCheck := antigravityCLICheck
	t.Cleanup(func() {
		antigravityCLILookPath = originalLookPath
		antigravityCLIRun = originalRun
		antigravityCLICheck = originalCheck
	})
	antigravityCLILookPath = func(string) (string, error) { return "/test/agy", nil }
	callCount := 0
	antigravityCLIRun = func(path string, args ...string) error {
		assert.Equal(t, "/test/agy", path)
		callCount++
		assert.Empty(t, args)
		return errors.New("login canceled")
	}
	antigravityCLICheck = func(string) error { t.Fatal("check must not be called"); return nil }

	err := authLoginAntigravityCLI()
	require.Error(t, err)
	assert.Equal(t, 1, callCount)
	assert.Contains(t, err.Error(), "antigravity-cli sign-in session failed")
}

func TestAuthLoginAntigravityCLISuccess(t *testing.T) {
	originalLookPath := antigravityCLILookPath
	originalRun := antigravityCLIRun
	originalCheck := antigravityCLICheck
	t.Cleanup(func() {
		antigravityCLILookPath = originalLookPath
		antigravityCLIRun = originalRun
		antigravityCLICheck = originalCheck
	})
	antigravityCLILookPath = func(name string) (string, error) {
		assert.Equal(t, "agy", name)
		return "/test/agy", nil
	}
	callCount := 0
	antigravityCLIRun = func(path string, args ...string) error {
		assert.Equal(t, "/test/agy", path)
		assert.Empty(t, args)
		callCount++
		return nil
	}
	antigravityCLICheck = func(path string) error {
		assert.Equal(t, "/test/agy", path)
		return nil
	}

	require.NoError(t, authLoginAntigravityCLI())
	assert.Equal(t, 1, callCount)
}

func TestLimitedBufferRejectsOversizedResponse(t *testing.T) {
	var output bytes.Buffer
	writer := &limitedBuffer{Buffer: &output, max: 3}

	_, err := writer.Write([]byte("toolong"))
	require.Error(t, err)
	assert.Empty(t, output.String())
}

func TestValidateAntigravityCLIAuthenticationResponse(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr string
	}{
		{name: "success", output: `{"is_error":false,"result":"authenticated"}`},
		{name: "invalid JSON", output: "not-json", wantErr: "invalid agy authentication response"},
		{name: "error response", output: `{"is_error":true,"result":"not signed in"}`, wantErr: "not signed in"},
		{name: "unexpected response", output: `{"is_error":false,"result":""}`, wantErr: "unexpected agy authentication response"},
		{name: "wrong case", output: `{"is_error":false,"result":"AUTHENTICATED"}`, wantErr: "unexpected agy authentication response"},
		{name: "surrounding whitespace", output: `{"is_error":false,"result":" authenticated "}`, wantErr: "unexpected agy authentication response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAntigravityCLIAuthenticationResponse([]byte(tt.output))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
