package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

// startBridgeServer spins up a minimal WebSocket server that collects every
// text frame it receives into a channel.
func startBridgeServer(t *testing.T) (*httptest.Server, chan map[string]any) {
	t.Helper()
	frames := make(chan map[string]any, 32)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				continue
			}
			frames <- frame
		}
	}))
	t.Cleanup(srv.Close)
	return srv, frames
}

// dialledChannel creates a WhatsAppChannel already connected to srv.
func dialledChannel(t *testing.T, srv *httptest.Server) *WhatsAppChannel {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp", config.WhatsAppSettings{}, messageBus, nil),
		config:      &config.WhatsAppSettings{},
		url:         wsURL,
		ctx:         context.Background(),
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })
	return ch
}

// recvFrame reads one frame with a generous timeout so tests don't flap under load.
func recvFrame(t *testing.T, frames chan map[string]any) map[string]any {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bridge frame")
		return nil
	}
}

// TestSend_BridgeProtocol verifies the exact JSON frame sent over the bridge WebSocket.
func TestSend_BridgeProtocol(t *testing.T) {
	srv, frames := startBridgeServer(t)
	ch := dialledChannel(t, srv)

	msg := bus.OutboundMessage{
		Channel: "whatsapp",
		ChatID:  "15551234567@s.whatsapp.net",
		Content: "hello world",
		Context: bus.InboundContext{Channel: "whatsapp", ChatID: "15551234567@s.whatsapp.net"},
	}
	if _, err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	frame := recvFrame(t, frames)

	if got := frame["type"]; got != "message" {
		t.Errorf("type=%q want %q", got, "message")
	}
	if got := frame["to"]; got != msg.ChatID {
		t.Errorf("to=%q want %q", got, msg.ChatID)
	}
	if got := frame["content"]; got != msg.Content {
		t.Errorf("content=%q want %q", got, msg.Content)
	}
}

// TestSend_NotRunning verifies ErrNotRunning when the channel is stopped.
func TestSend_NotRunning(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp", config.WhatsAppSettings{}, messageBus, nil),
		ctx:         context.Background(),
	}
	_, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "x", Content: "y"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error=%v; want ErrNotRunning", err)
	}
}

// TestSendMedia_NotRunning verifies ErrNotRunning when the channel is stopped.
func TestSendMedia_NotRunning(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp", config.WhatsAppSettings{}, messageBus, nil),
		ctx:         context.Background(),
	}
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Parts: []bus.MediaPart{{Type: "image", Ref: "media://x"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error=%v; want ErrNotRunning", err)
	}
}

// TestSendMedia_NoMediaStore verifies ErrSendFailed when no media store is configured.
func TestSendMedia_NoMediaStore(t *testing.T) {
	srv, _ := startBridgeServer(t)
	ch := dialledChannel(t, srv)
	// Deliberately leave media store nil.

	msg := bus.OutboundMediaMessage{
		Channel: "whatsapp",
		ChatID:  "15551234567@s.whatsapp.net",
		Parts:   []bus.MediaPart{{Type: "image", Ref: "media://abc"}},
	}
	_, err := ch.SendMedia(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no media store") {
		t.Errorf("error=%v; want 'no media store'", err)
	}
}

// TestSendMedia_BridgeProtocol verifies the exact JSON frames sent for each MediaPart.
func TestSendMedia_BridgeProtocol(t *testing.T) {
	srv, frames := startBridgeServer(t)
	ch := dialledChannel(t, srv)

	store := media.NewFileMediaStore()

	imgDir := t.TempDir()
	imgPath := filepath.Join(imgDir, "photo.jpg")
	if err := os.WriteFile(imgPath, []byte("imgdata"), 0o600); err != nil {
		t.Fatalf("write img: %v", err)
	}
	imgRef, err := store.Store(imgPath, media.MediaMeta{Filename: "photo.jpg", ContentType: "image/jpeg"}, "scope1")
	if err != nil {
		t.Fatalf("store img: %v", err)
	}

	audioDir := t.TempDir()
	audioPath := filepath.Join(audioDir, "clip.ogg")
	if err := os.WriteFile(audioPath, []byte("audiodata"), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	audioRef, err := store.Store(audioPath, media.MediaMeta{Filename: "clip.ogg", ContentType: "audio/ogg"}, "scope1")
	if err != nil {
		t.Fatalf("store audio: %v", err)
	}

	ch.SetMediaStore(store)

	chatID := "15551234567@s.whatsapp.net"
	msg := bus.OutboundMediaMessage{
		Channel: "whatsapp",
		ChatID:  chatID,
		Parts: []bus.MediaPart{
			{Type: "image", Ref: imgRef, Caption: "look at this", Filename: "photo.jpg"},
			{Type: "audio", Ref: audioRef, Filename: "clip.ogg"},
		},
	}
	if _, err := ch.SendMedia(context.Background(), msg); err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	// First frame: image
	f0 := recvFrame(t, frames)
	if got := f0["type"]; got != "media" {
		t.Errorf("[0] type=%q want %q", got, "media")
	}
	if got := f0["to"]; got != chatID {
		t.Errorf("[0] to=%q want %q", got, chatID)
	}
	if got := f0["media_type"]; got != "image" {
		t.Errorf("[0] media_type=%q want %q", got, "image")
	}
	if got, _ := f0["path"].(string); got != imgPath {
		t.Errorf("[0] path=%q want %q", got, imgPath)
	}
	if got := f0["caption"]; got != "look at this" {
		t.Errorf("[0] caption=%q want %q", got, "look at this")
	}
	if got := f0["filename"]; got != "photo.jpg" {
		t.Errorf("[0] filename=%q want %q", got, "photo.jpg")
	}

	// Second frame: audio (no caption → field must be absent)
	f1 := recvFrame(t, frames)
	if got := f1["type"]; got != "media" {
		t.Errorf("[1] type=%q want %q", got, "media")
	}
	if got := f1["media_type"]; got != "audio" {
		t.Errorf("[1] media_type=%q want %q", got, "audio")
	}
	if _, ok := f1["caption"]; ok {
		t.Error("[1] caption should be absent when empty")
	}
}

// TestSendMedia_SkipsBadRef verifies that an unresolvable ref is skipped and the rest are sent.
func TestSendMedia_SkipsBadRef(t *testing.T) {
	srv, frames := startBridgeServer(t)
	ch := dialledChannel(t, srv)

	store := media.NewFileMediaStore()
	dir := t.TempDir()
	path := filepath.Join(dir, "img.png")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	goodRef, err := store.Store(path, media.MediaMeta{}, "scope2")
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	ch.SetMediaStore(store)

	msg := bus.OutboundMediaMessage{
		Channel: "whatsapp",
		ChatID:  "chat1",
		Parts: []bus.MediaPart{
			{Type: "file", Ref: "media://does-not-exist"},
			{Type: "image", Ref: goodRef},
		},
	}
	if _, err := ch.SendMedia(context.Background(), msg); err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	f := recvFrame(t, frames)
	if got := f["media_type"]; got != "image" {
		t.Errorf("media_type=%q want %q", got, "image")
	}

	// Confirm no second frame arrives.
	select {
	case extra := <-frames:
		t.Errorf("expected no more frames, got: %v", extra)
	default:
	}
}
