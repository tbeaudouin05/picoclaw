//go:build whatsapp_native

package whatsapp

import (
	"path/filepath"
	"testing"
)

func TestResolveDBPath(t *testing.T) {
	tests := []struct {
		name       string
		storePath  string
		wantDBPath string
		wantDir    string
	}{
		{
			name:       "directory path appends store.db",
			storePath:  "/var/lib/picoclaw/whatsapp",
			wantDBPath: "/var/lib/picoclaw/whatsapp/store.db",
			wantDir:    "/var/lib/picoclaw/whatsapp",
		},
		{
			name:       "relative directory path appends store.db",
			storePath:  "whatsapp",
			wantDBPath: "whatsapp/store.db",
			wantDir:    "whatsapp",
		},
		{
			name:       "explicit .db path used directly",
			storePath:  "/var/lib/picoclaw/session.db",
			wantDBPath: "/var/lib/picoclaw/session.db",
			wantDir:    "/var/lib/picoclaw",
		},
		{
			name:       "relative .db path used directly",
			storePath:  "mystore.db",
			wantDBPath: "mystore.db",
			wantDir:    ".",
		},
		{
			name:       "nested .db path used directly with correct parent dir",
			storePath:  "/data/wa/custom.db",
			wantDBPath: "/data/wa/custom.db",
			wantDir:    "/data/wa",
		},
		{
			name:       "path that happens to end in store.db treated as file",
			storePath:  "/some/dir/store.db",
			wantDBPath: "/some/dir/store.db",
			wantDir:    "/some/dir",
		},
		{
			name:       "directory path does not create nested store.db/store.db",
			storePath:  "/tmp/whatsapp",
			wantDBPath: filepath.Join("/tmp/whatsapp", "store.db"),
			wantDir:    "/tmp/whatsapp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDBPath, gotDir := resolveDBPath(tt.storePath)
			if gotDBPath != tt.wantDBPath {
				t.Errorf("dbPath = %q, want %q", gotDBPath, tt.wantDBPath)
			}
			if gotDir != tt.wantDir {
				t.Errorf("dir = %q, want %q", gotDir, tt.wantDir)
			}
		})
	}
}
