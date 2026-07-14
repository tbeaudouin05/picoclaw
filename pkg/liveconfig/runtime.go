package liveconfig

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

type Runtime struct {
	store          Store
	cfg            config.LiveConfig
	unavailableErr error
	mu             sync.Mutex
	init           bool
}

func NewRuntime(cfg config.LiveConfig) (*Runtime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.Driver.Turso == nil {
		return nil, fmt.Errorf("live_config driver.turso is required")
	}
	store, err := NewTursoHTTPStore(
		cfg.Driver.Turso.URL,
		cfg.Driver.Turso.AuthToken.String(),
		schemaFromConfig(cfg.Driver.Turso.Schema),
	)
	if err != nil {
		return nil, err
	}
	return &Runtime{store: store, cfg: cfg}, nil
}

func NewUnavailableRuntime(cfg config.LiveConfig, err error) *Runtime {
	if err == nil {
		err = fmt.Errorf("live config unavailable")
	}
	return &Runtime{cfg: cfg, unavailableErr: err}
}

func (r *Runtime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *Runtime) Store() Store {
	if r == nil {
		return nil
	}
	return r.store
}

func (r *Runtime) RecordID() string {
	if r == nil {
		return MainRecordID
	}
	if strings.TrimSpace(r.cfg.RecordID) == "" {
		return MainRecordID
	}
	return strings.TrimSpace(r.cfg.RecordID)
}

func (r *Runtime) AdminUpdatesEnabled() bool { return r != nil && r.cfg.AdminUpdatesEnabled }

func (r *Runtime) AdminUpdateChannels() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.cfg.AdminUpdateChannels...)
}

func (r *Runtime) EnsureInitialized(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.init {
		return nil
	}
	if err := r.store.InitSchema(ctx); err != nil {
		return err
	}
	r.init = true
	return nil
}

func (r *Runtime) ShouldInject(channel string) bool {
	if r == nil || !r.cfg.Enabled {
		return false
	}
	if len(r.cfg.InjectChannels) == 0 {
		return true
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	for _, allowed := range r.cfg.InjectChannels {
		if strings.ToLower(strings.TrimSpace(allowed)) == channel {
			return true
		}
	}
	return false
}

func (r *Runtime) RuntimePrompt(ctx context.Context) (string, error) {
	if r != nil && r.unavailableErr != nil {
		return "", r.unavailableErr
	}
	if err := r.EnsureInitialized(ctx); err != nil {
		return "", err
	}
	rec, err := r.store.GetRecord(ctx, r.RecordID())
	if err != nil {
		return "", err
	}
	return BuildRuntimePrompt(rec), nil
}

func schemaFromConfig(cfg config.LiveConfigSchema) Schema {
	return Schema{
		Table:         cfg.Table,
		IDColumn:      cfg.IDColumn,
		VersionColumn: cfg.VersionColumn,
		UpdatedColumn: cfg.UpdatedColumn,
		PayloadColumn: cfg.PayloadColumn,
	}
}
