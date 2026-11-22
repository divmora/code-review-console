package execsettings

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
)

// Executor runs each task with the settings saved in the admin panel: it
// rebuilds the underlying executor when they change, so a new version or
// node selector applies to the next review without a restart.
type Executor struct {
	svc    *Service
	build  func(config.Executor) (executor.Executor, error)
	logger *slog.Logger

	mu  sync.Mutex
	key string
	cur executor.Executor
}

// NewExecutor wraps base (built from the config file). build makes an
// executor for changed settings; for Kubernetes it should reuse base's
// client (see executor.Kubernetes.With).
func NewExecutor(svc *Service, base executor.Executor, build func(config.Executor) (executor.Executor, error), logger *slog.Logger) *Executor {
	return &Executor{svc: svc, build: build, logger: logger, key: key(svc.Config()), cur: base}
}

func key(e config.Executor) string {
	b, _ := json.Marshal(e)
	return string(b)
}

// Name is the executor type.
func (e *Executor) Name() string { return e.svc.Type() }

// Current returns the executor for the settings saved now.
func (e *Executor) Current(ctx context.Context) (executor.Executor, error) {
	eff, err := e.svc.Effective(ctx)
	if err != nil {
		// The database is unreachable: keep running with what we have.
		e.logger.Warn("executor settings: using the last known settings", "error", err)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.cur, nil
	}
	k := key(eff)
	e.mu.Lock()
	defer e.mu.Unlock()
	if k == e.key {
		return e.cur, nil
	}
	next, err := e.build(eff)
	if err != nil {
		return nil, &executor.RunError{Message: "the executor settings saved in the admin panel don't work: " + err.Error()}
	}
	e.key, e.cur = k, next
	e.logger.Info("executor settings changed in the admin panel: applying them from this review on", "type", eff.Type)
	return next, nil
}

// Run runs t with the current settings.
func (e *Executor) Run(ctx context.Context, t executor.Task) (*executor.Result, error) {
	cur, err := e.Current(ctx)
	if err != nil {
		return nil, err
	}
	return cur.Run(ctx, t)
}
