package executor

import "testing"

func TestParseOutputScores(t *testing.T) {
	res, err := ParseOutput("review", []byte(`{"posted":1,"scores":{"quality":85,"security":100,"maintainability":88}}`))
	if err != nil || res.Scores == nil || *res.Scores != (Scores{Quality: 85, Security: 100, Maintainability: 88}) {
		t.Fatalf("scores are read: %v %+v", err, res)
	}
	for name, out := range map[string]string{
		"older CLI":    `{"posted":1}`,
		"out of range": `{"scores":{"quality":0,"security":100,"maintainability":88}}`,
		"too high":     `{"scores":{"quality":85,"security":101,"maintainability":88}}`,
		"skipped":      `{"skipped":true,"skip_reason":"draft","scores":{"quality":85,"security":100,"maintainability":88}}`,
	} {
		if res, err := ParseOutput("review", []byte(out)); err != nil || res.Scores != nil {
			t.Errorf("%s: no scores: %v %+v", name, err, res.Scores)
		}
	}
}
