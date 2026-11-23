package executor

import (
	"os"
	"regexp"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

// TestDocumentedNetworkPolicy keeps the NetworkPolicy in docs/deploy.md
// valid and selecting the label review pods carry.
func TestDocumentedNetworkPolicy(t *testing.T) {
	b, err := os.ReadFile("../../docs/deploy.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "### Review pods' network access")
	if i < 0 {
		t.Fatal("section missing")
	}
	m := regexp.MustCompile("(?s)```yaml\n(apiVersion: networking.k8s.io/v1\n.*?)```").FindStringSubmatch(doc[i:])
	if m == nil {
		t.Fatal("NetworkPolicy example missing")
	}
	var np networkingv1.NetworkPolicy
	if err := yaml.UnmarshalStrict([]byte(m[1]), &np); err != nil {
		t.Fatalf("the documented NetworkPolicy doesn't parse: %v", err)
	}
	if np.Kind != "NetworkPolicy" || np.Spec.PodSelector.MatchLabels[RoleLabel] != RoleReview {
		t.Errorf("it must select %s=%s: %+v", RoleLabel, RoleReview, np.Spec.PodSelector)
	}
	if len(np.Spec.Egress) != 2 || np.Spec.Egress[1].To[0].IPBlock == nil || !strings.Contains(strings.Join(np.Spec.Egress[1].To[0].IPBlock.Except, " "), "169.254.0.0/16") {
		t.Errorf("DNS plus HTTPS, without the metadata range: %+v", np.Spec.Egress)
	}
}
