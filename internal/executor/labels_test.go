package executor

import (
	"strings"
	"testing"
)

func TestParseLabels(t *testing.T) {
	review := `{"summary":"s","reviewed":["a.go"],"model":"m","usage":{"InputTokens":1500,"OutputTokens":200},
		"labels":{"mode":"apply","suggested":[{"name":"type::bug","reason":"fix"},{"name":"priority::high","suggest_only":true}],"applied":["type::bug"],"removed":["type::feature"]},
		"labels_usage":{"InputTokens":400,"OutputTokens":30}}`
	res, err := ParseOutput("review", []byte(review))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Labels, ",") != "type::bug,priority::high" || res.LabelsMode != "apply" || res.LabelsTokens != 430 ||
		strings.Join(res.LabelsApplied, ",") != "type::bug" || strings.Join(res.LabelsRemoved, ",") != "type::feature" {
		t.Errorf("labels: %+v", res)
	}
	// Off (no field): nil, so nothing is recorded; on with no pick: empty.
	if res, _ := ParseOutput("review", []byte(`{"summary":"s"}`)); res.Labels != nil {
		t.Error("labels off")
	}
	if res, _ := ParseOutput("review", []byte(`{"summary":"s","labels":{"mode":"suggest","suggested":[]}}`)); res.Labels == nil || len(res.Labels) != 0 {
		t.Error("labels on, none picked")
	}
}
