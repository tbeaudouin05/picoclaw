package runtimecmd

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

// fakeMediaLoop records the MediaStore injected by the runtime inject-turn path.
type fakeMediaLoop struct {
	store    media.MediaStore
	storeSet bool
}

func (f *fakeMediaLoop) SetMediaStore(s media.MediaStore) {
	f.store = s
	f.storeSet = true
}

// TestConfigureInjectedTurnMedia_InjectsStoreWhenSendFileEnabled is the
// regression for the Mist runtime send_file failure: admin inject-turn ran the
// agent loop without ever wiring a MediaStore, so send_file aborted with
// "media store not configured". The runtime command path must initialize and
// inject a MediaStore whenever send_file is enabled.
func TestConfigureInjectedTurnMedia_InjectsStoreWhenSendFileEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.SendFile.Enabled = true

	loop := &fakeMediaLoop{}
	store := configureInjectedTurnMedia(loop, cfg)
	defer stopMediaStore(store)

	if !loop.storeSet {
		t.Fatal("send_file enabled but no MediaStore was injected into the agent loop")
	}
	if loop.store == nil {
		t.Fatal("MediaStore injected into the agent loop is nil")
	}
	if store == nil {
		t.Fatal("configureInjectedTurnMedia returned nil store while send_file is enabled")
	}
}

// TestConfigureInjectedTurnMedia_NoStoreWhenMediaToolsDisabled ensures we only
// pay for a MediaStore when a media-emitting tool is actually enabled.
func TestConfigureInjectedTurnMedia_NoStoreWhenMediaToolsDisabled(t *testing.T) {
	cfg := &config.Config{}

	loop := &fakeMediaLoop{}
	store := configureInjectedTurnMedia(loop, cfg)
	defer stopMediaStore(store)

	if loop.storeSet {
		t.Fatal("no media tools enabled but a MediaStore was injected")
	}
	if store != nil {
		t.Fatal("no media tools enabled but configureInjectedTurnMedia returned a store")
	}
}

func stopMediaStore(store media.MediaStore) {
	if fms, ok := store.(*media.FileMediaStore); ok {
		fms.Stop()
	}
}
