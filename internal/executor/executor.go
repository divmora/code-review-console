// Package executor runs the codereview CLI for one job: as a local
// process, a Docker container or a Kubernetes Job. Every executor uses the
// same contract: arguments and environment in, JSON on stdout, logs on
// stderr, exit code 0 on success.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Task is one CLI invocation.
type Task struct {
	ID   string // unique, used for container/Job names
	Kind string // "review", "diagram", "ask" or "validate"
	URL  string // pull/merge request web URL

	Full     bool // review every file, ignore previous reviews
	Auto     bool // apply auto_review rules (push-triggered)
	Post     bool // publish to the PR
	Commands bool // advertise @bot commands in the summary
	// ScoreLabels and CommitStatus pass --score-labels / --commit-status.
	ScoreLabels  bool
	CommitStatus bool
	// Approval passes --approve and its rule (nil: no approval).
	Approval *Approval
	Question string

	// Overrides are admin settings, passed as CODEREVIEW_OVERRIDES.
	Overrides any
	// ConfigYAML is the engine config. It references secrets as ${VAR};
	// it must not contain secret values.
	ConfigYAML []byte
	// Env holds secrets and settings for the job (tokens, provider keys).
	Env map[string]string
}

// Result is what a run produced.
type Result struct {
	Skipped    bool
	SkipReason string
	// Raw is the CLI's JSON output for reviews.
	Raw       json.RawMessage
	Posted    int
	Rejected  int
	Files     int
	TokensIn  int
	TokensOut int
	Model     string
	Partial   bool // some batches failed
	TooLarge  bool
	Answer    string // for ask
	// Scores are the review's scores (nil when the CLI gave none).
	Scores *Scores
	// Approval is what the CLI did with the bot's approval (approved,
	// already_approved, withdrawn, not_approved, unchanged, failed; empty
	// without --approve), and ApprovalReason why.
	Approval       string
	ApprovalReason string
	// Diagram: the review drew (or kept) a flow diagram; DiagramTokens is
	// what drawing it cost (in and out); DiagramNote why none was drawn.
	Diagram       bool
	DiagramTokens int
	DiagramNote   string
	// Unreadable is set when the CLI succeeded but its result couldn't be
	// read in full (see parseMerged); the counts are a best effort.
	Unreadable string
	// Logs is the tail of stderr.
	Logs string
}

// Scores rate a review's change from 1 to 100 (the CLI's "scores").
type Scores struct {
	Quality         int `json:"quality"`
	Security        int `json:"security"`
	Maintainability int `json:"maintainability"`
}

// Approval is the bot's approval rule (see settings.Stored.Approve).
type Approval struct {
	// Block is the lowest severity that blocks approval, or "none".
	Block    string
	MinScore int
	Withdraw bool
}

func firstNonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Executor runs tasks.
type Executor interface {
	Name() string
	Run(ctx context.Context, t Task) (*Result, error)
}

// Args builds the CLI arguments for a task. Secrets never appear here.
func Args(t Task) ([]string, error) {
	switch t.Kind {
	case "review":
		args := []string{"review", t.URL, "--format", "json"}
		if t.Post {
			args = append(args, "--post")
		}
		if t.Full {
			args = append(args, "--full")
		}
		if t.Auto {
			args = append(args, "--auto")
		}
		if t.Commands {
			args = append(args, "--commands")
		}
		if t.ScoreLabels {
			args = append(args, "--score-labels")
		}
		if t.CommitStatus {
			args = append(args, "--commit-status")
		}
		if a := t.Approval; a != nil {
			args = append(args, "--approve", "--approve-block", firstNonEmpty(a.Block, "none"), "--approve-min-score", strconv.Itoa(a.MinScore))
			// Always explicit: the CLI's default changed to on in 0.6, so
			// "leave it" must say =false (older CLIs accept that too).
			args = append(args, "--approve-withdraw="+strconv.FormatBool(a.Withdraw))
		}
		return args, nil
	case "diagram":
		// "@bot diagram": draw the flow for the current head from the last
		// review, and replace the diagram in the summary (CLI 0.7.0+).
		args := []string{"review", t.URL, "--diagram-only", "--format", "json"}
		if t.Post {
			args = append(args, "--post")
		}
		return args, nil
	case "validate":
		// Checks the job config and overrides with the binary that runs reviews.
		return []string{"config", "validate", "--format", "json"}, nil
	case "learn":
		// Reads CODEREVIEW_LEARN_INPUT; prints whether it holds a learning.
		return []string{"learn"}, nil
	case "ask":
		if strings.TrimSpace(t.Question) == "" {
			return nil, errors.New("ask: empty question")
		}
		args := []string{"ask", t.URL, t.Question}
		if t.Post {
			args = append(args, "--post")
		}
		return args, nil
	}
	return nil, fmt.Errorf("unknown task kind %q", t.Kind)
}

// Env builds the job environment: config, overrides and secrets.
func Env(t Task) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	if len(t.ConfigYAML) > 0 {
		env["CODEREVIEW_CONFIG_YAML"] = string(t.ConfigYAML)
	}
	if t.Overrides != nil {
		b, err := json.Marshal(t.Overrides)
		if err != nil {
			return nil, err
		}
		if string(b) != "null" && string(b) != "{}" {
			env["CODEREVIEW_OVERRIDES"] = string(b)
		}
	}
	return env, nil
}

// ParseOutput turns CLI stdout into a Result.
func ParseOutput(kind string, stdout []byte) (*Result, error) {
	res := &Result{}
	switch kind {
	case "ask":
		res.Answer = strings.TrimSpace(string(stdout))
		return res, nil
	case "validate", "learn":
		out := bytes.TrimSpace(stdout)
		if !json.Valid(out) {
			return nil, errors.New("codereview config validate produced no JSON (is the CLI older than config validate?)")
		}
		res.Raw = json.RawMessage(out)
		return res, nil
	}
	out := bytes.TrimSpace(stdout)
	if len(out) == 0 {
		return nil, errors.New("codereview produced no output")
	}
	var v struct {
		// "skipped" is true when the whole PR was skipped
		// ({"skipped": true, "skip_reason": ...}); in a review result it is
		// the list of skipped files. Accept both.
		Skipped    json.RawMessage `json:"skipped"`
		SkipReason string          `json:"skip_reason"`
		Posted     int             `json:"posted"`
		Rejected   int             `json:"rejected"`
		Reviewed   []any           `json:"reviewed"`
		Model      string          `json:"model"`
		Errors     []any           `json:"errors"`
		TooLarge   bool            `json:"too_large"`
		Scores     *Scores         `json:"scores"`
		Approval   string          `json:"approval"`
		ApprovalR  string          `json:"approval_reason"`
		Usage      struct {
			InputTokens  int `json:"InputTokens"`
			OutputTokens int `json:"OutputTokens"`
		} `json:"usage"`
		Diagram      json.RawMessage `json:"diagram"`
		DiagramNote  string          `json:"diagram_note"`
		DiagramUsage struct {
			InputTokens  int `json:"InputTokens"`
			OutputTokens int `json:"OutputTokens"`
		} `json:"diagram_usage"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("parse codereview output: %w", err)
	}
	res.Skipped, res.SkipReason = string(bytes.TrimSpace(v.Skipped)) == "true", v.SkipReason
	if !res.Skipped {
		res.Raw = json.RawMessage(out)
	}
	res.Posted, res.Rejected, res.Files, res.Model = v.Posted, v.Rejected, len(v.Reviewed), v.Model
	res.TokensIn, res.TokensOut = v.Usage.InputTokens, v.Usage.OutputTokens
	res.Partial, res.TooLarge = len(v.Errors) > 0, v.TooLarge
	res.Approval, res.ApprovalReason = v.Approval, v.ApprovalR
	d := bytes.TrimSpace(v.Diagram)
	res.Diagram = len(d) > 0 && string(d) != "null"
	res.DiagramTokens, res.DiagramNote = v.DiagramUsage.InputTokens+v.DiagramUsage.OutputTokens, v.DiagramNote
	if sc := v.Scores; !res.Skipped && sc != nil && in1to100(sc.Quality) && in1to100(sc.Security) && in1to100(sc.Maintainability) {
		res.Scores = sc
	}
	return res, nil
}

func in1to100(v int) bool { return v >= 1 && v <= 100 }

// tailBuffer keeps the last n bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func newTail(n int) *tailBuffer { return &tailBuffer{n: n} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// lastErrorLine extracts the CLI's "error: ..." line from stderr.
func lastErrorLine(logs string) string {
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "error: ") {
			return strings.TrimPrefix(lines[i], "error: ")
		}
	}
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		return lines[len(lines)-1]
	}
	return ""
}

// RunError is a failed CLI run.
type RunError struct {
	ExitCode int
	Message  string
	Logs     string
}

func (e *RunError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("codereview exited with code %d", e.ExitCode)
}

// passthrough are environment variables forwarded from the console's own
// environment to every job when set: provider keys and cloud credentials.
var passthrough = []string{
	"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
	"GEMINI_API_KEY", "GOOGLE_API_KEY", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_BASE_URL",
	"OPENROUTER_API_KEY", "GROQ_API_KEY", "DEEPSEEK_API_KEY", "MISTRAL_API_KEY", "XAI_API_KEY", "TOGETHER_API_KEY",
	"LITELLM_API_KEY", "LITELLM_BASE_URL", "OLLAMA_BASE_URL", "OPENAI_COMPATIBLE_API_KEY", "OPENAI_COMPATIBLE_BASE_URL",
	"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE",
}

// PassthroughNames returns the default names plus extra.
func PassthroughNames(extra ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append(append([]string{}, passthrough...), extra...) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
