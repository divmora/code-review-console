package executor

import "testing"

func TestParseDiagram(t *testing.T) {
	review := `{"summary":"s","reviewed":["a.go"],"model":"m","usage":{"InputTokens":1500,"OutputTokens":200},
		"diagram":{"kind":"sequence","mermaid":"sequenceDiagram"},"diagram_usage":{"InputTokens":700,"OutputTokens":90}}`
	res, err := ParseOutput("review", []byte(review))
	if err != nil || !res.Diagram || res.DiagramTokens != 790 || res.TokensIn != 1500 {
		t.Fatalf("review with a diagram: %+v %v", res, err)
	}
	only := `{"diagram_only":true,"diagram":null,"diagram_note":"the model found no flow to draw","diagram_usage":{"InputTokens":300,"OutputTokens":5},"usage":{"InputTokens":300,"OutputTokens":5},"model":"m"}`
	res, err = ParseOutput("diagram", []byte(only))
	if err != nil || res.Diagram || res.DiagramNote != "the model found no flow to draw" || res.DiagramTokens != 305 || res.Model != "m" {
		t.Fatalf("diagram-only without a diagram: %+v %v", res, err)
	}
	if res, _ := ParseOutput("review", []byte(`{"summary":"s"}`)); res.Diagram {
		t.Error("no diagram field: none")
	}
}
