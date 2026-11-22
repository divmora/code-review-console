package executor

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/divmora/code-review-console/internal/config"
)

func TestJobSpecServiceAccountToken(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.KubeExecutor
		want bool
	}{
		{"no service account", config.KubeExecutor{}, false},
		{"service account alone", config.KubeExecutor{ServiceAccount: "reviewer"}, false},
		{"explicit opt-in", config.KubeExecutor{ServiceAccount: "reviewer", AutomountServiceAccountToken: true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := NewKubernetesWithClient(c.cfg, nil, fake.NewClientset())
			job := k.jobSpec("crb-x", "ns", nil, nil, context.Background())
			got := job.Spec.Template.Spec.AutomountServiceAccountToken
			if got == nil || *got != c.want {
				t.Fatalf("automountServiceAccountToken = %v, want %v", got, c.want)
			}
			if sa := job.Spec.Template.Spec.ServiceAccountName; sa != c.cfg.ServiceAccount {
				t.Fatalf("serviceAccountName = %q, want %q", sa, c.cfg.ServiceAccount)
			}
		})
	}
}

func TestOwnSecretSetsOwnerReference(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "crb-x", Namespace: "ns"}})
	k := NewKubernetesWithClient(config.KubeExecutor{Namespace: "ns"}, nil, cs)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "crb-x", Namespace: "ns", UID: "uid-1"}}
	if err := k.ownSecret(ctx, "crb-x", job); err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("ns").Get(ctx, "crb-x", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs := sec.OwnerReferences
	if len(refs) != 1 || refs[0].Kind != "Job" || refs[0].Name != "crb-x" || refs[0].UID != "uid-1" || refs[0].APIVersion != "batch/v1" {
		t.Fatalf("ownerReferences = %+v", refs)
	}
}
