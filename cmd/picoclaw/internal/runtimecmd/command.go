package runtimecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	picointernal "github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/liveconfig"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type updateRequest struct {
	Path                  string         `json:"path"`
	Value                 any            `json:"value"`
	Delete                bool           `json:"delete"`
	ExpectedConfigVersion any            `json:"expected_config_version"`
	Updates               map[string]any `json:"updates"`
	Deletes               []string       `json:"deletes"`
}

func NewRuntimeCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "runtime", Hidden: true, Args: cobra.NoArgs}
	admin := &cobra.Command{Use: "admin", Short: "Hidden admin runtime helper group, including primary smoke-test prompt injection", Hidden: true, Args: cobra.NoArgs}
	customer := &cobra.Command{Use: "customer", Short: "Hidden customer runtime helper group, including primary smoke-test prompt injection", Hidden: true, Args: cobra.NoArgs}
	admin.AddCommand(newRuntimeConfigCommand(), newInjectTurnCommand("admin", "telegram"))
	customer.AddCommand(newInjectTurnCommand("customer", "whatsapp"))
	cmd.AddCommand(admin, customer)
	return cmd
}

func newRuntimeConfigCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "runtime-config", Hidden: true, Args: cobra.NoArgs}
	cmd.AddCommand(newRuntimeConfigGetCommand(), newRuntimeConfigUpdateCommand())
	return cmd
}

func newRuntimeConfigGetCommand() *cobra.Command {
	var adminSlug string
	cmd := &cobra.Command{
		Use:    "get",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, recordID, err := loadRuntimeStore()
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			defer store.Close()
			if err := store.InitSchema(cmd.Context()); err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			rec, err := store.GetRecord(cmd.Context(), recordID)
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			return writeRecord(cmd.OutOrStdout(), rec)
		},
	}
	cmd.Flags().Bool("direct", false, "run as direct runtime helper")
	cmd.Flags().StringVar(&adminSlug, "admin-slug", "", "admin role slug")
	_ = adminSlug
	return cmd
}

func newRuntimeConfigUpdateCommand() *cobra.Command {
	var adminSlug string
	cmd := &cobra.Command{
		Use:    "update",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, recordID, err := loadRuntimeStore()
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			defer store.Close()
			if err := store.InitSchema(cmd.Context()); err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			var req updateRequest
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&req); err != nil && err != io.EOF {
				return writeRuntimeError(cmd.OutOrStdout(), fmt.Errorf("decode update request: %w", err))
			}
			current, err := store.GetRecord(cmd.Context(), recordID)
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			updates := map[string]any{}
			for k, v := range req.Updates {
				updates[k] = v
			}
			if strings.TrimSpace(req.Path) != "" && !req.Delete {
				updates[req.Path] = req.Value
			}
			deletes := append([]string{}, req.Deletes...)
			if strings.TrimSpace(req.Path) != "" && req.Delete {
				deletes = append(deletes, req.Path)
			}
			if len(updates) == 0 && len(deletes) == 0 {
				return writeRuntimeError(cmd.OutOrStdout(), fmt.Errorf("no updates or deletes requested"))
			}
			next, changed, err := liveconfig.ApplyDotPathChanges(current.ConfigJSON, updates, deletes)
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			expected := expectedVersion(req.ExpectedConfigVersion, current.ConfigVersion)
			updated, err := store.UpdateRecord(context.Background(), current.ID, expected, next)
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			out := map[string]any{"status": "ok", "record_id": updated.ID, "config_version": updated.ConfigVersion, "updated_at": updated.UpdatedAt, "changed_paths": changed}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		},
	}
	cmd.Flags().Bool("direct", false, "run as direct runtime helper")
	cmd.Flags().StringVar(&adminSlug, "admin-slug", "", "admin role slug")
	_ = adminSlug
	return cmd
}

func loadRuntimeStore() (*liveconfig.TursoHTTPStore, string, error) {
	cfg, err := picointernal.LoadConfig()
	if err != nil {
		return nil, "", err
	}
	lc := cfg.LiveConfig
	if !lc.Enabled || lc.Driver.Turso == nil {
		return nil, "", fmt.Errorf("live config store is not configured")
	}
	schema := liveconfig.SchemaFromConfig(lc.Driver.Turso.Schema)
	store, err := liveconfig.NewTursoHTTPStore(lc.Driver.Turso.URL, lc.Driver.Turso.AuthToken.String(), schema)
	if err != nil {
		return nil, "", err
	}
	recordID := strings.TrimSpace(lc.RecordID)
	if recordID == "" {
		recordID = liveconfig.MainRecordID
	}
	return store, recordID, nil
}

func writeRecord(w io.Writer, rec *liveconfig.Record) error {
	var cfg map[string]any
	if err := json.Unmarshal(rec.ConfigJSON, &cfg); err != nil {
		return err
	}
	out := map[string]any{"status": "ok", "record_id": rec.ID, "config_id": rec.ID, "config_version": rec.ConfigVersion, "updated_at": rec.UpdatedAt, "config": cfg}
	return json.NewEncoder(w).Encode(out)
}

func writeRuntimeError(w io.Writer, err error) error {
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": err.Error()})
	return fmt.Errorf("runtime helper failed: %w", err)
}

func expectedVersion(raw any, fallback int64) int64 {
	switch v := raw.(type) {
	case float64:
		if v > 0 {
			return int64(v)
		}
	case int:
		if v > 0 {
			return int64(v)
		}
	case int64:
		if v > 0 {
			return v
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

type injectTurnRequest struct {
	Text        string `json:"text"`
	SessionKey  string `json:"session_key"`
	ChatID      string `json:"chat_id"`
	SenderID    string `json:"sender_id"`
	DisplayName string `json:"display_name"`
}

func newInjectTurnCommand(role, channel string) *cobra.Command {
	var adminSlug string
	var customerSlug string
	cmd := &cobra.Command{
		Use:    "inject-turn",
		Short:  fmt.Sprintf("Inject one %s prompt turn as the primary smoke-test path", role),
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var req injectTurnRequest
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&req); err != nil && err != io.EOF {
				return writeRuntimeError(cmd.OutOrStdout(), fmt.Errorf("decode inject-turn request: %w", err))
			}
			if strings.TrimSpace(req.Text) == "" {
				return writeRuntimeError(cmd.OutOrStdout(), fmt.Errorf("inject-turn text is required"))
			}
			if strings.TrimSpace(req.SessionKey) == "" {
				req.SessionKey = fmt.Sprintf("runtime:%s:%s", role, channel)
			}
			if strings.TrimSpace(req.ChatID) == "" {
				req.ChatID = fmt.Sprintf("runtime-%s", role)
			}
			if strings.TrimSpace(req.SenderID) == "" {
				req.SenderID = fmt.Sprintf("runtime-%s", role)
			}
			response, err := injectTurnRunner(cmd.Context(), channel, req)
			if err != nil {
				return writeRuntimeError(cmd.OutOrStdout(), err)
			}
			return writeInjectTurnResponse(cmd.OutOrStdout(), channel, response, req)
		},
	}
	cmd.Flags().Bool("direct", false, "run as direct runtime helper")
	cmd.Flags().StringVar(&adminSlug, "admin-slug", "", "admin role slug")
	cmd.Flags().StringVar(&customerSlug, "customer-slug", "", "customer role slug")
	_ = adminSlug
	_ = customerSlug
	return cmd
}

var injectTurnRunner = runInjectedTurn

func runInjectedTurn(ctx context.Context, channel string, req injectTurnRequest) (string, error) {
	cfg, err := picointernal.LoadConfig()
	if err != nil {
		return "", err
	}
	logger.ConfigureFromEnv()
	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return "", err
	}
	if modelID != "" {
		cfg.Agents.Defaults.ModelName = modelID
	}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	loop := agent.NewAgentLoop(cfg, msgBus, provider)
	defer loop.Close()
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   req.ChatID,
			ChatType: "direct",
			SenderID: req.SenderID,
		},
		Sender:     bus.SenderInfo{DisplayName: req.DisplayName},
		Content:    req.Text,
		SessionKey: req.SessionKey,
	}
	return loop.ProcessInjectedMessage(ctx, msg)
}

func writeInjectTurnResponse(w io.Writer, channel, response string, req injectTurnRequest) error {
	out := map[string]any{
		"status":       "ok",
		"channel":      channel,
		"response":     response,
		"session_key":  req.SessionKey,
		"chat_id":      req.ChatID,
		"sender_id":    req.SenderID,
		"display_name": req.DisplayName,
	}
	return json.NewEncoder(w).Encode(out)
}
