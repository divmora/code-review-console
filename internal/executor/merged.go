package executor

import (
	"regexp"
	"strconv"
	"strings"
)

// Pod logs merge stdout and stderr. The container runtime splits a stdout
// line longer than its log line limit (16 KiB in containerd) into partial
// records, and a stderr record can land between them. The kubelet joins the
// partial records in file order, so whole stderr lines (slog, text or JSON)
// show up inside the result, in the middle of a JSON string or between
// tokens (issue #39).

// logStart matches the start of a codereview log line in slog's text or
// JSON format.
var logStart = regexp.MustCompile(`time=\d{4}-\d{2}-\d{2}T\S+ level=(?:DEBUG|INFO|WARN|ERROR)\b|\{"time":"\d{4}-\d{2}-\d{2}T[^"]*","level":"(?:DEBUG|INFO|WARN|ERROR)"`)

// stripLogLines removes log lines that were spliced into stdout. A log line
// runs from its "time=" to the end of its line; the text before it on that
// line is a fragment of a stdout line whose rest starts on the next line, so
// the two are joined again.
func stripLogLines(s string) (clean string, removed []string) {
	lines := strings.SplitAfter(s, "\n")
	var b strings.Builder
	for _, line := range lines {
		loc := logStart.FindStringIndex(line)
		if loc == nil || !strings.HasSuffix(line, "\n") {
			b.WriteString(line)
			continue
		}
		b.WriteString(line[:loc[0]])
		removed = append(removed, strings.TrimSuffix(line[loc[0]:], "\n"))
	}
	return b.String(), removed
}

// parseMerged is ParseOutput for output read from merged pod logs. Log lines
// found inside the result are removed (and returned, to be kept with the
// logs). If a review's result still can't be read, the review itself ran
// (the CLI exited 0, so it posted): it is reported as done, with a note and
// whatever counts could be found, instead of failed.
func parseMerged(kind, stdout string) (*Result, []string, error) {
	if kind == "ask" {
		clean, removed := stripLogLines(stdout)
		res, err := ParseOutput(kind, []byte(clean))
		return res, removed, err
	}
	res, err := ParseOutput(kind, []byte(stdout))
	if err == nil {
		return res, nil, nil
	}
	clean, removed := stripLogLines(stdout)
	if len(removed) > 0 {
		if res, err2 := ParseOutput(kind, []byte(clean)); err2 == nil {
			return res, removed, nil
		}
	}
	if kind != "review" || strings.TrimSpace(stdout) == "" {
		return nil, removed, err
	}
	return salvage(clean, err), removed, nil
}

var (
	// The CLI prints the result indented by two spaces per level; the
	// top-level keys are the ones at two spaces ("model" also appears in
	// "verification", one level down). Token counts appear only in "usage".
	topInt = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^  "` + name + `": (\d+)`)
	}
	postedRe   = topInt("posted")
	rejectedRe = topInt("rejected")
	inRe       = regexp.MustCompile(`"InputTokens":\s*(\d+)`)
	outRe      = regexp.MustCompile(`"OutputTokens":\s*(\d+)`)
	modelRe    = regexp.MustCompile(`(?m)^  "model": "([^"\\]{1,200})"`)
)

// salvage reads what it can from a review result that isn't valid JSON.
func salvage(out string, cause error) *Result {
	num := func(re *regexp.Regexp) int {
		if m := re.FindStringSubmatch(out); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 0
	}
	res := &Result{Posted: num(postedRe), Rejected: num(rejectedRe), TokensIn: num(inRe), TokensOut: num(outRe),
		Unreadable: "the review ran, but its result could not be read (" + cause.Error() + "); counts may be incomplete"}
	if m := modelRe.FindStringSubmatch(out); m != nil {
		res.Model = m[1]
	}
	return res
}
