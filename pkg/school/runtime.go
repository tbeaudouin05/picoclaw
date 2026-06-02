package school

import (
	"context"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

type Runtime struct {
	store Store
	cfg   config.RuntimeSchoolConfig
	mu    sync.Mutex
	init  bool
}

func NewRuntime(cfg config.RuntimeSchoolConfig) (*Runtime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	store, err := NewTursoHTTPStore(cfg.TursoURL, cfg.TursoAuthToken.String())
	if err != nil {
		return nil, err
	}
	return &Runtime{store: store, cfg: cfg}, nil
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

func (r *Runtime) SchoolConfigID() string {
	if r == nil {
		return MainConfigID
	}
	if strings.TrimSpace(r.cfg.ConfigID) == "" {
		return MainConfigID
	}
	return strings.TrimSpace(r.cfg.ConfigID)
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
	if err := r.EnsureInitialized(ctx); err != nil {
		return "", err
	}
	cfg, err := r.store.GetConfig(ctx, r.SchoolConfigID())
	if err != nil {
		return "", err
	}
	return BuildRuntimePrompt(cfg), nil
}
