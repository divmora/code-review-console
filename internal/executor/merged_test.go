package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// cliResult is a review result as the CLI prints it: a map re-marshalled
// with two-space indent, so keys are sorted, and a summary longer than
// containerd's 16 KiB log line limit.
func cliResult(t *testing.T) string {
	t.Helper()
	m := map[string]any{
		"summary":      strings.Repeat("Walkthrough of the change, with a table. ", 1200), // ~49 KiB on one line
		"reviewed":     []string{"a.go", "b.go", "c.go"},
		"posted":       4,
		"rejected":     1,
		"model":        "bedrock:claude",
		"usage":        map[string]int{"InputTokens": 38000, "OutputTokens": 2100},
		"verification": map[string]any{"ran": true, "model": "bedrock:verifier"},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b) + "\n"
}

// mergeLogs is what the kubelet returns when stderr records are written
// while the runtime splits a long stdout line into 16 KiB partial records:
// each log line lands at a partial-record boundary.
func mergeLogs(stdout string, logs ...string) string {
	const limit = 16 << 10
	var b strings.Builder
	n := 0
	for _, line := range strings.SplitAfter(stdout, "\n") {
		for len(line) > limit && n < len(logs) {
			b.WriteString(line[:limit])
			b.WriteString(logs[n] + "\n")
			line, n = line[limit:], n+1
		}
		b.WriteString(line)
	}
	return b.String()
}

const marker = "@@CODEREVIEW-RESULT-x@@"

func TestInterleavedLogsInResult(t *testing.T) {
	out := cliResult(t)
	logs := []string{
		`time=2026-11-22T08:59:22.101Z level=INFO msg="published review" pr=g/p!7 comments=4`,
		`time=2026-11-22T08:59:22.102Z level=WARN msg="slow model" took=31s`,
	}
	merged := mergeLogs(out, logs...)
	// One more log line between two whole stdout lines, and logs around.
	merged = strings.Replace(merged, "  \"rejected\"", `{"time":"2026-11-22T08:59:22Z","level":"INFO","msg":"json log"}`+"\n  \"rejected\"", 1)
	pod := "time=2026-11-22T08:58:00Z level=INFO msg=start\n\n" + marker + "\n" + merged + "\n" + marker + "-END\n"

	stdout, stderr := splitMarker(pod, marker)
	if _, err := ParseOutput("review", []byte(stdout)); err == nil {
		t.Fatal("the test must reproduce the failure first")
	}
	res, removed, err := parseMerged("review", stdout)
	if err != nil || res.Unreadable != "" {
		t.Fatalf("parse: %v %+v", err, res)
	}
	if res.Posted != 4 || res.Rejected != 1 || res.Files != 3 || res.TokensIn != 38000 || res.TokensOut != 2100 || res.Model != "bedrock:claude" {
		t.Errorf("result: %+v", res)
	}
	var back map[string]any
	_ = json.Unmarshal(res.Raw, &back)
	if back["summary"] != strings.Repeat("Walkthrough of the change, with a table. ", 1200) {
		t.Error("the summary must come back intact")
	}
	if len(removed) != 3 || removed[1] != logs[0] || removed[2] != logs[1] || !strings.Contains(stderr, "msg=start") {
		t.Errorf("removed log lines are kept for the logs: %q", removed)
	}
}

func TestCleanResultUntouched(t *testing.T) {
	// A summary that quotes a log line is left alone when the JSON is valid.
	b, _ := json.MarshalIndent(map[string]any{"summary": "saw time=2026-11-22T08:59:22Z level=INFO msg=x\n in the logs", "posted": 1}, "", "  ")
	res, removed, err := parseMerged("review", string(b)+"\n")
	if err != nil || len(removed) != 0 || res.Posted != 1 || !strings.Contains(string(res.Raw), "level=INFO") {
		t.Fatalf("%v %q %+v", err, removed, res)
	}
}

func TestUnreadableResultIsSalvaged(t *testing.T) {
	// Cut mid-way (e.g. a log line that was itself split): not JSON even
	// after removing log lines. The CLI exited 0, so the review is not a
	// failure; the counts that can be found are kept.
	out := cliResult(t)
	broken := out[:strings.Index(out, `"summary"`)+5000] + "\n" + out[strings.Index(out, `  "usage"`):]
	res, _, err := parseMerged("review", broken)
	if err != nil || res.Unreadable == "" {
		t.Fatalf("want a salvaged result: %v %+v", err, res)
	}
	if res.Posted != 4 || res.Rejected != 1 || res.TokensIn != 38000 || res.TokensOut != 2100 || res.Model != "bedrock:claude" {
		t.Errorf("salvaged counts (top-level model, not the verifier's): %+v", res)
	}
	if _, _, err := parseMerged("review", "  \n"); err == nil {
		t.Error("no output at all is still an error")
	}
	if _, _, err := parseMerged("validate", "not json"); err == nil {
		t.Error("only reviews are salvaged")
	}
}

func TestAskAnswerLosesLogLines(t *testing.T) {
	res, removed, err := parseMerged("ask", "The cache is per request.\ntime=2026-11-22T08:59:22Z level=INFO msg=posted\nSo it's safe.\n")
	if err != nil || res.Answer != "The cache is per request.\nSo it's safe." || len(removed) != 1 {
		t.Fatalf("%v %q %q", err, res.Answer, removed)
	}
}
