package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

func TestRun_StartupFailuresReturnErrorAndEmitStructuredLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepare    func(t *testing.T, dir string) string
		wantErr    string
		wantLogSub string
	}{
		{
			name: "invalid config returns load error",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				cfgPath := filepath.Join(dir, "invalid-config.json")
				if err := os.WriteFile(cfgPath, []byte("{invalid-json"), 0o644); err != nil {
					t.Fatalf("WriteFile(invalid config) error = %v", err)
				}
				return cfgPath
			},
			wantErr:    "error loading config:",
			wantLogSub: "error loading config:",
		},
		{
			name: "invalid config returns pre-check error",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				cfg := config.DefaultConfig()
				cfg.Gateway.Port = 0
				cfgPath := filepath.Join(dir, "config.json")
				if err := config.SaveConfig(cfgPath, cfg); err != nil {
					t.Fatalf("SaveConfig() error = %v", err)
				}
				return cfgPath
			},
			wantErr:    "config pre-check failed: invalid gateway port: 0",
			wantLogSub: "config pre-check failed: invalid gateway port: 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			configPath := tt.prepare(t, homeDir)

			cmd := exec.Command(os.Args[0], "-test.run=TestGatewayRunStartupFailureHelper")
			cmd.Env = append(os.Environ(),
				"GO_WANT_GATEWAY_RUN_HELPER=1",
				"PICO_TEST_HOME="+homeDir,
				"PICO_TEST_CONFIG="+configPath,
			)

			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper exited unexpectedly: %v\noutput:\n%s", err, string(output))
			}

			out := string(output)
			if !strings.Contains(out, tt.wantErr) {
				t.Fatalf("helper output missing expected error substring %q:\n%s", tt.wantErr, out)
			}

			logData, readErr := os.ReadFile(filepath.Join(homeDir, logPath, logFile))
			if readErr != nil {
				t.Fatalf("ReadFile(gateway.log) error = %v", readErr)
			}
			logText := string(logData)
			if !strings.Contains(logText, "Gateway startup failed") {
				t.Fatalf("gateway.log missing structured startup failure log:\n%s", logText)
			}
			if !strings.Contains(logText, tt.wantLogSub) {
				t.Fatalf("gateway.log missing expected failure detail %q:\n%s", tt.wantLogSub, logText)
			}
		})
	}
}

func TestGatewayRunStartupFailureHelper(t *testing.T) {
	if os.Getenv("GO_WANT_GATEWAY_RUN_HELPER") != "1" {
		return
	}

	homeDir := os.Getenv("PICO_TEST_HOME")
	configPath := os.Getenv("PICO_TEST_CONFIG")

	err := Run(false, homeDir, configPath, false)
	if err == nil {
		fmt.Fprintln(os.Stdout, "expected startup error, got nil")
		os.Exit(2)
	}

	fmt.Fprintln(os.Stdout, err.Error())
	os.Exit(0)
}

func TestCollectGatewayStartupStatusHandlesMalformedInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		startupInfo         map[string]any
		wantToolsCount      int
		wantSkillsAvailable int
		wantSkillsTotal     int
		wantLogFields       map[string]any
	}{
		{
			name:          "missing info",
			startupInfo:   map[string]any{},
			wantLogFields: map[string]any{},
		},
		{
			name: "wrong map shapes",
			startupInfo: map[string]any{
				"tools":  "unexpected",
				"skills": []any{"unexpected"},
			},
			wantLogFields: map[string]any{},
		},
		{
			name: "valid startup info",
			startupInfo: map[string]any{
				"tools": map[string]any{
					"count": 3,
				},
				"skills": map[string]any{
					"available": 2,
					"total":     5,
				},
			},
			wantToolsCount:      3,
			wantSkillsAvailable: 2,
			wantSkillsTotal:     5,
			wantLogFields: map[string]any{
				"tools_count":      3,
				"skills_available": 2,
				"skills_total":     5,
			},
		},
		{
			name: "json number startup info",
			startupInfo: map[string]any{
				"tools": map[string]any{
					"count": float64(4),
				},
				"skills": map[string]any{
					"available": float64(1),
					"total":     float64(6),
				},
			},
			wantToolsCount:      4,
			wantSkillsAvailable: 1,
			wantSkillsTotal:     6,
			wantLogFields: map[string]any{
				"tools_count":      4,
				"skills_available": 1,
				"skills_total":     6,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := collectGatewayStartupStatus(tt.startupInfo)
			if got.toolsCount != tt.wantToolsCount {
				t.Fatalf("toolsCount = %d, want %d", got.toolsCount, tt.wantToolsCount)
			}
			if got.skillsAvailable != tt.wantSkillsAvailable {
				t.Fatalf("skillsAvailable = %d, want %d", got.skillsAvailable, tt.wantSkillsAvailable)
			}
			if got.skillsTotal != tt.wantSkillsTotal {
				t.Fatalf("skillsTotal = %d, want %d", got.skillsTotal, tt.wantSkillsTotal)
			}
			if !reflect.DeepEqual(got.logFields, tt.wantLogFields) {
				t.Fatalf("logFields = %#v, want %#v", got.logFields, tt.wantLogFields)
			}
		})
	}
}

func TestPublishGatewayEvent(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	t.Cleanup(func() {
		if err := eventBus.Close(); err != nil {
			t.Fatalf("Close runtime event bus: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindGatewayStart).SubscribeChan(
		ctx,
		runtimeevents.SubscribeOptions{Name: "gateway-test", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sub.Close(); err != nil {
			t.Fatalf("Close subscription: %v", err)
		}
	})

	al := agent.NewAgentLoop(
		config.DefaultConfig(),
		bus.NewMessageBus(),
		&startupBlockedProvider{reason: "not used"},
		agent.WithRuntimeEvents(eventBus),
	)
	t.Cleanup(al.Close)

	startedAt := time.Now().Add(-1500 * time.Millisecond)
	publishGatewayEvent(al, runtimeevents.KindGatewayStart, startedAt, nil)

	evt := receiveGatewayRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindGatewayStart ||
		evt.Source.Component != "gateway" ||
		evt.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("gateway event = %+v", evt)
	}
	payload, ok := evt.Payload.(gatewayEventPayload)
	if !ok {
		t.Fatalf("payload type = %T, want gatewayEventPayload", evt.Payload)
	}
	if payload.DurationMS <= 0 {
		t.Fatalf("DurationMS = %d, want positive", payload.DurationMS)
	}
	if evt.Attrs["duration_ms"] == nil {
		t.Fatalf("gateway event attrs missing duration_ms: %#v", evt.Attrs)
	}
}

func TestShutdownGatewayClosesMessageBus(t *testing.T) {
	msgBus := bus.NewMessageBus()
	al := agent.NewAgentLoop(
		config.DefaultConfig(),
		msgBus,
		&startupBlockedProvider{reason: "not used"},
	)
	msgBus.SetEventPublisher(al.RuntimeEventBus())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, eventsCh, err := al.RuntimeEventBus().Channel().OfKind(runtimeevents.KindBusCloseCompleted).SubscribeChan(
		ctx,
		runtimeevents.SubscribeOptions{Name: "bus-close-test", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	defer func() {
		_ = sub.Close()
	}()

	shutdownGateway(&services{}, al, &startupBlockedProvider{reason: "not used"}, msgBus, true)

	evt := receiveGatewayRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindBusCloseCompleted {
		t.Fatalf("shutdown event kind = %q, want %q", evt.Kind, runtimeevents.KindBusCloseCompleted)
	}
	if err := msgBus.PublishVoiceControl(context.Background(), bus.VoiceControl{}); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("PublishVoiceControl after shutdown error = %v, want %v", err, bus.ErrBusClosed)
	}
}

func receiveGatewayRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt := <-ch:
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway runtime event")
		return runtimeevents.Event{}
	}
}

func gatewayHasCronTool(al *agent.AgentLoop) bool {
	info := al.GetStartupInfo()
	toolsInfo, ok := info["tools"].(map[string]any)
	if !ok {
		return false
	}
	names, ok := toolsInfo["names"].([]string)
	if !ok {
		return false
	}
	for _, n := range names {
		if n == "cron" {
			return true
		}
	}
	return false
}

// TestSetupCronTool_RegistersToolWithoutStartingScheduler verifies the gateway
// builds its cron runtime through the shared builder: the cron tool is
// registered on the agent loop, but setupCronTool itself does not start the
// scheduler (the caller starts it explicitly afterwards).
func TestSetupCronTool_RegistersToolWithoutStartingScheduler(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = true

	al := agent.NewAgentLoop(cfg, bus.NewMessageBus(), &startupBlockedProvider{reason: "not used"})
	t.Cleanup(al.Close)

	cronService, err := setupCronTool(al, bus.NewMessageBus(), t.TempDir(), true, 0, cfg)
	if err != nil {
		t.Fatalf("setupCronTool() error: %v", err)
	}
	t.Cleanup(cronService.Stop)

	if cronService.IsRunning() {
		t.Fatal("setupCronTool must not start the scheduler; the caller does that")
	}
	if !gatewayHasCronTool(al) {
		t.Fatal("expected cron tool to be registered on the agent loop")
	}
}

// TestSetupCronTool_OmitsToolWhenDisabled verifies the gateway path leaves the
// cron tool unregistered when cron is disabled, while still returning a service.
func TestSetupCronTool_OmitsToolWhenDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = false

	al := agent.NewAgentLoop(cfg, bus.NewMessageBus(), &startupBlockedProvider{reason: "not used"})
	t.Cleanup(al.Close)

	cronService, err := setupCronTool(al, bus.NewMessageBus(), t.TempDir(), true, 0, cfg)
	if err != nil {
		t.Fatalf("setupCronTool() error: %v", err)
	}
	if cronService == nil {
		t.Fatal("expected non-nil cron service even when cron is disabled")
	}
	t.Cleanup(cronService.Stop)

	if gatewayHasCronTool(al) {
		t.Fatal("expected no cron tool registered when cron is disabled")
	}
}
