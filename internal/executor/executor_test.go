package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArgsAndEnv(t *testing.T) {
	args, err := Args(Task{Kind: "review", URL: "https://github.com/o/r/pull/1", Post: true, Full: true, Commands: true})
	if err != nil || strings.Join(args, " ") != "review https://github.com/o/r/pull/1 --format json --post --full --commands" {
		t.Errorf("review args = %v %v", args, err)
	}
	args, _ = Args(Task{Kind: "ask", URL: "u", Question: "why?", Post: true})
	if strings.Join(args, "|") != "ask|u|why?|--post" {
		t.Errorf("ask args = %v", args)
	}
	if _, err := Args(Task{Kind: "ask", URL: "u"}); err == nil {
		t.Error("empty question should fail")
	}
	if _, err := Args(Task{Kind: "nope"}); err == nil {
		t.Error("unknown kind should fail")
	}
	env, _ := Env(Task{ConfigYAML: []byte("model: x\n"), Overrides: map[string]any{"profile": "chill"}, Env: map[string]string{"GITHUB_TOKEN": "t"}})
	if env["CODEREVIEW_CONFIG_YAML"] != "model: x\n" || env["CODEREVIEW_OVERRIDES"] != `{"profile":"chill"}` || env["GITHUB_TOKEN"] != "t" {
		t.Errorf("env = %v", env)
	}
	for _, a := range args {
		if strings.Contains(a, "GITHUB_TOKEN") {
			t.Error("secrets must not be in args")
		}
	}
}

func TestParseOutput(t *testing.T) {
	res, err := ParseOutput("review", []byte(`{"reviewed":["a","b"],"model":"m","posted":3,"rejected":1,"errors":["x"],"usage":{"InputTokens":10,"OutputTokens":2}}`))
	if err != nil || res.Files != 2 || res.Posted != 3 || !res.Partial || res.TokensIn != 10 || res.Raw == nil {
		t.Errorf("res = %+v %v", res, err)
	}
	res, _ = ParseOutput("review", []byte(`{"skipped":true,"skip_reason":"draft"}`))
	if !res.Skipped || res.SkipReason != "draft" || res.Raw != nil {
		t.Errorf("skip = %+v", res)
	}
	if _, err := ParseOutput("review", nil); err == nil {
		t.Error("empty output should fail")
	}
	res, _ = ParseOutput("ask", []byte("  the answer \n"))
	if res.Answer != "the answer" {
		t.Errorf("answer = %q", res.Answer)
	}
}

func TestSplitMarkerAndJobName(t *testing.T) {
	logs := "time=1 level=INFO msg=reviewing\n\n@@M@@\n{\"ok\":true}\n"
	out, errs := splitMarker(logs, "@@M@@")
	if out != "{\"ok\":true}\n" || !strings.Contains(errs, "reviewing") {
		t.Errorf("split: %q / %q", out, errs)
	}
	out, errs = splitMarker("log1\n@@M@@\n{\"ok\":true}\n@@M@@-END\ntime=2 msg=published\n", "@@M@@")
	if out != "{\"ok\":true}\n" || !strings.Contains(errs, "log1") || !strings.Contains(errs, "published") {
		t.Errorf("end marker split: %q / %q", out, errs)
	}
	if out, errs := splitMarker("only logs", "@@M@@"); out != "" || errs != "only logs" {
		t.Errorf("no marker: %q %q", out, errs)
	}
	n := jobName("Org/Some_Very.Long-Repository-Name-That-Goes-On-And-On-123-456789")
	if len(n) > 52 || !strings.HasPrefix(n, "crb-org-some-very-long") || strings.ContainsAny(n, "_./") {
		t.Errorf("job name %q", n)
	}
}

// fakeCLI writes a shell script standing in for the codereview binary.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codereview")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalSuccessAndIsolation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://console-secret")
	t.Setenv("OPENAI_API_KEY", "sk-pass")
	bin := fakeCLI(t, `
[ -n "$DATABASE_URL" ] && { echo "error: console secret leaked" >&2; exit 1; }
[ "$OPENAI_API_KEY" = "sk-pass" ] || { echo "error: passthrough missing" >&2; exit 1; }
[ "$GITHUB_TOKEN" = "ghs_job" ] || { echo "error: job token missing" >&2; exit 1; }
[ "$1" = "review" ] || { echo "error: bad args $*" >&2; exit 1; }
echo "time=x level=INFO msg=reviewing" >&2
echo '{"reviewed":["a.go"],"posted":2,"model":"m","usage":{"InputTokens":5,"OutputTokens":1}}'
`)
	l := &Local{Binary: bin, Pass: PassthroughNames()}
	res, err := l.Run(context.Background(), Task{ID: "1", Kind: "review", URL: "https://github.com/o/r/pull/1", Post: true, Env: map[string]string{"GITHUB_TOKEN": "ghs_job"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Posted != 2 || res.Files != 1 || !strings.Contains(res.Logs, "reviewing") {
		t.Errorf("res = %+v", res)
	}
}

func TestLocalFailure(t *testing.T) {
	bin := fakeCLI(t, `echo "some log" >&2; echo "error: get pull request: HTTP 404" >&2; exit 1`)
	_, err := (&Local{Binary: bin}).Run(context.Background(), Task{ID: "1", Kind: "review", URL: "u"})
	var re *RunError
	if !errors.As(err, &re) || re.ExitCode != 1 || re.Message != "get pull request: HTTP 404" {
		t.Fatalf("err = %#v", err)
	}
	if _, err := (&Local{Binary: "/nonexistent/codereview"}).Run(context.Background(), Task{Kind: "review", URL: "u"}); err == nil {
		t.Error("missing binary should fail")
	}
}

func TestLocalCancelKillsProcess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "still-running")
	bin := fakeCLI(t, `sleep 30; touch `+marker)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&Local{Binary: bin}).Run(ctx, Task{Kind: "review", URL: "u"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want deadline error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancel took %s", time.Since(start))
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("child process survived cancellation")
	}
}

func TestParseOutputSkippedFilesList(t *testing.T) {
	out := `{"too_large":true,"posted":0,"skipped":[{"path":"a.php","reason":"deleted"}],"reviewed":[],"model":"m"}`
	res, err := ParseOutput("review", []byte(out))
	if err != nil {
		t.Fatalf("a list of skipped files must parse: %v", err)
	}
	if res.Skipped || !res.TooLarge || res.Raw == nil {
		t.Errorf("skipped files are not a skipped PR: %+v", res)
	}
	res, err = ParseOutput("review", []byte(`{"skipped":true,"skip_reason":"draft"}`))
	if err != nil || !res.Skipped || res.SkipReason != "draft" {
		t.Errorf("whole-PR skip: %+v %v", res, err)
	}
}

func TestArgsPostingFlags(t *testing.T) {
	args, _ := Args(Task{Kind: "review", URL: "u", Post: true, ScoreLabels: true, CommitStatus: true})
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--score-labels") || !strings.Contains(got, "--commit-status") {
		t.Errorf("posting flags missing: %s", got)
	}
	args, _ = Args(Task{Kind: "review", URL: "u"})
	if strings.Contains(strings.Join(args, " "), "--score-labels") {
		t.Error("flags must be opt-in")
	}
}

func TestArgsApproval(t *testing.T) {
	args, _ := Args(Task{Kind: "review", URL: "u", Post: true, Approval: &Approval{Block: "major", MinScore: 80, Withdraw: true}})
	if got := strings.Join(args, " "); !strings.Contains(got, "--approve --approve-block major --approve-min-score 80 --approve-withdraw=true") {
		t.Errorf("approval flags: %s", got)
	}
	args, _ = Args(Task{Kind: "review", URL: "u", Post: true, Approval: &Approval{}})
	if got := strings.Join(args, " "); !strings.Contains(got, "--approve-block none --approve-min-score 0 --approve-withdraw=false") {
		t.Errorf("approve whatever was found, without withdrawing (explicit: the CLI's default is on): %s", got)
	}
	args, _ = Args(Task{Kind: "review", URL: "u", Post: true})
	if strings.Contains(strings.Join(args, " "), "--approve") {
		t.Error("approval is opt-in")
	}
	res, err := ParseOutput("review", []byte(`{"posted":0,"approval":"withdrawn","approval_reason":"1 finding(s) at major or above"}`))
	if err != nil || res.Approval != "withdrawn" || res.ApprovalReason == "" {
		t.Errorf("approval outcome: %v %+v", err, res)
	}
}
