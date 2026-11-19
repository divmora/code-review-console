package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"
)

// Local runs the CLI as a child process of the console.
type Local struct {
	Binary string
	// Pass lists env var names copied from the console's environment.
	Pass []string
}

func (l *Local) Name() string { return "local" }

func (l *Local) Run(ctx context.Context, t Task) (*Result, error) {
	args, err := Args(t)
	if err != nil {
		return nil, err
	}
	env, err := Env(t)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, l.Binary, args...)
	// A clean environment: only what the job needs, never the console's
	// own secrets (database URL, OIDC secret, GitHub App key).
	cmd.Env = baseEnv()
	for _, n := range l.Pass {
		if v, ok := os.LookupEnv(n); ok {
			cmd.Env = append(cmd.Env, n+"="+v)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On cancel, stop the whole process group gracefully, then kill.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	var stdout bytes.Buffer
	stderr := newTail(16 << 10)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	logs := stderr.String()
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return nil, runErr // binary missing, etc.
		}
		return nil, &RunError{ExitCode: ee.ExitCode(), Message: lastErrorLine(logs), Logs: logs}
	}
	res, err := ParseOutput(t.Kind, stdout.Bytes())
	if err != nil {
		return nil, &RunError{Message: err.Error(), Logs: logs}
	}
	res.Logs = logs
	return res, nil
}

// baseEnv is the minimal environment a CLI process needs.
func baseEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"HOME", "TMPDIR", "TZ", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
