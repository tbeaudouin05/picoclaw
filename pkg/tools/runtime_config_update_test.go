package tools

import "testing"

func TestRuntimeConfigUpdateToolUsesGenericPublicName(t *testing.T) {
	t.Parallel()

	tool := NewRuntimeConfigUpdateTool(nil, "main", []string{"telegram"}, nil)

	if got, want := tool.Name(), "update_runtime_config"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
}
