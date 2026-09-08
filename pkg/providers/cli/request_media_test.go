package cliprovider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

func useTestMediaDir(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	dir := media.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrepareCLIImageInputsScopesAndRewritesRelevantImages(t *testing.T) {
	sourceDir := useTestMediaDir(t)
	first := filepath.Join(sourceDir, "first.png")
	second := filepath.Join(sourceDir, "second.jpg")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, dir, cleanup, err := prepareCLIImageInputs(
		"look [image:"+first+"] and [image: photo]",
		"again [image:"+first+"] then [image:"+second+"]",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if dir == "" || filepath.Clean(dir) == filepath.Clean(sourceDir) {
		t.Fatalf("request media dir = %q, want a distinct scoped directory", dir)
	}
	if strings.Contains(strings.Join(prepared, "\n"), first) || strings.Contains(strings.Join(prepared, "\n"), second) {
		t.Fatalf("prepared prompts retained source paths: %q", prepared)
	}
	if !strings.Contains(prepared[0], "[image: photo]") {
		t.Fatalf("non-path image placeholder changed: %q", prepared[0])
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("scoped directory has %d files, want two unique inputs", len(entries))
	}
}

func TestPrepareCLIImageInputsLeavesPromptWithoutManagedMediaUnchanged(t *testing.T) {
	parts := []string{"plain prompt", "placeholder [image: photo]", "workspace [image:/workspace/photo.png]"}
	prepared, dir, cleanup, err := prepareCLIImageInputs(parts...)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if dir != "" || strings.Join(prepared, "\x00") != strings.Join(parts, "\x00") {
		t.Fatalf("prepared = %q, dir = %q; want unchanged prompt and no extra directory", prepared, dir)
	}
}

func TestAppendAddDirsPairsAndDeduplicates(t *testing.T) {
	args := appendAddDirs([]string{"--sandbox", "--add-dir", "/workspace"}, "/workspace/.", "/media", "/media")
	want := []string{"--sandbox", "--add-dir", "/workspace", "--add-dir", "/media"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q, want paired deduplicated flags %q", args, want)
	}
}

func TestClaudeCLIImageAccessArgsInJSONAndStreamJSON(t *testing.T) {
	image := filepath.Join(useTestMediaDir(t), "image.png")
	if err := os.WriteFile(image, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "json"},
		{name: "stream-json", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args")
			p := NewClaudeCliProvider(t.TempDir())
			if tc.stream {
				p.command = createStreamArgCaptureCLI(t, argsFile, "{\"type\":\"result\",\"result\":\"ok\"}\n")
				if _, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "see [image:" + image + "]"}}, nil, "", nil, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				p.command = createArgCaptureCLI(t, argsFile)
				if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "see [image:" + image + "]"}}, nil, "", nil); err != nil {
					t.Fatal(err)
				}
			}
			assertSingleScopedAddDir(t, argsFile, image)
		})
	}
}

func TestAntigravityCLIImageAccessArgsInJSONAndStreamJSON(t *testing.T) {
	image := filepath.Join(useTestMediaDir(t), "image.png")
	if err := os.WriteFile(image, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "json"},
		{name: "stream-json", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			argsFile := filepath.Join(state, "args")
			output := `{"status":"SUCCESS","response":"ok"}`
			if tc.stream {
				output = "{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"ok\"}}\n"
			}
			p := NewAntigravityCliProvider("")
			p.command = createMockAntigravityCLI(t, argsFile, filepath.Join(state, "prompt"), filepath.Join(state, "cwd"), output)
			messages := []Message{{Role: "user", Content: "see [image:" + image + "]"}}
			if tc.stream {
				if _, err := p.ChatStreamEvents(context.Background(), messages, nil, "", nil, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := p.Chat(context.Background(), messages, nil, "", nil); err != nil {
					t.Fatal(err)
				}
			}
			assertSingleScopedAddDir(t, argsFile, image)
		})
	}
}

func TestCLIProvidersFailImagePreparationBeforeLaunch(t *testing.T) {
	missing := filepath.Join(useTestMediaDir(t), "missing.png")
	marker := filepath.Join(t.TempDir(), "launched")
	script := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	messages := []Message{{Role: "user", Content: "see [image:" + missing + "]"}}

	claude := NewClaudeCliProvider("")
	claude.command = script
	if _, err := claude.Chat(context.Background(), messages, nil, "", nil); err == nil || !strings.Contains(err.Error(), "prepare CLI image input") {
		t.Fatalf("Claude Chat error = %v, want explicit preparation error", err)
	}
	antigravity := NewAntigravityCliProvider("")
	antigravity.command = script
	if _, err := antigravity.ChatStreamEvents(context.Background(), messages, nil, "", nil, nil); err == nil || !strings.Contains(err.Error(), "prepare CLI image input") {
		t.Fatalf("Antigravity stream error = %v, want explicit preparation error", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("subprocess launched before preparation failure; marker stat error = %v", err)
	}
}

func assertSingleScopedAddDir(t *testing.T, argsFile, sourceImage string) {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(raw))
	count := 0
	var dir string
	for i := range args {
		if args[i] == "--add-dir" {
			count++
			if i+1 >= len(args) {
				t.Fatalf("unpaired --add-dir in %q", args)
			}
			dir = args[i+1]
		}
	}
	if count != 1 {
		t.Fatalf("--add-dir count = %d, want one in %q", count, args)
	}
	if filepath.Clean(dir) == filepath.Clean(filepath.Dir(sourceImage)) {
		t.Fatalf("granted source/global directory %q instead of request-scoped directory", dir)
	}
}
