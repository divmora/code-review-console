package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"time"
)

// Docker runs each task in a fresh container from the CLI image.
type Docker struct {
	Binary  string // docker CLI, default "docker"
	Image   string
	Network string
	CPUs    string
	Memory  string
	Pass    []string
}

func (d *Docker) Name() string { return "docker" }

var safeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func (d *Docker) Run(ctx context.Context, t Task) (*Result, error) {
	args, err := Args(t)
	if err != nil {
		return nil, err
	}
	env, err := Env(t)
	if err != nil {
		return nil, err
	}
	name := "crb-" + safeName.ReplaceAllString(t.ID, "-")
	run := []string{"run", "--rm", "--name", name, "--read-only", "--tmpfs", "/tmp", "--security-opt", "no-new-privileges", "--cap-drop", "ALL", "--pids-limit", "256"}
	if d.Network != "" {
		run = append(run, "--network", d.Network)
	}
	if d.CPUs != "" {
		run = append(run, "--cpus", d.CPUs)
	}
	if d.Memory != "" {
		run = append(run, "--memory", d.Memory)
	}
	// Values travel through the docker client's environment ("-e NAME"
	// without "=value"), so secrets never appear in the process list.
	childEnv := baseEnv()
	if v, ok := os.LookupEnv("DOCKER_HOST"); ok {
		childEnv = append(childEnv, "DOCKER_HOST="+v)
	}
	for _, n := range d.Pass {
		if v, ok := os.LookupEnv(n); ok {
			run = append(run, "-e", n)
			childEnv = append(childEnv, n+"="+v)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		run = append(run, "-e", k)
		childEnv = append(childEnv, k+"="+env[k])
	}
	run = append(run, d.Image)
	run = append(run, args...)

	cmd := exec.Command(d.Binary, run...)
	cmd.Env = childEnv
	var stdout bytes.Buffer
	stderr := newTail(16 << 10)
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var runErr error
	done := make(chan struct{})
	go func() { runErr = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		// Killing the docker client would leave the container running.
		rm := exec.Command(d.Binary, "rm", "-f", name)
		rm.Env = childEnv
		_ = rm.Run()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
	logs := stderr.String()
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return nil, runErr
		}
		msg := lastErrorLine(logs)
		if ee.ExitCode() == 125 {
			msg = "docker could not start the review container: " + msg
		}
		return nil, &RunError{ExitCode: ee.ExitCode(), Message: msg, Logs: logs}
	}
	res, err := ParseOutput(t.Kind, stdout.Bytes())
	if err != nil {
		return nil, &RunError{Message: err.Error(), Logs: logs}
	}
	res.Logs = logs
	return res, nil
}
