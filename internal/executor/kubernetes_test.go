package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
			k, err := NewKubernetesWithClient(c.cfg, nil, fake.NewClientset())
			if err != nil {
				t.Fatal(err)
			}
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
	k, err := NewKubernetesWithClient(config.KubeExecutor{Namespace: "ns"}, nil, cs)
	if err != nil {
		t.Fatal(err)
	}
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

// kubeFromYAML decodes an executor.kubernetes block the way config.Load does.
func kubeFromYAML(t *testing.T, body string) config.KubeExecutor {
	t.Helper()
	var c config.KubeExecutor
	if err := yaml.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestJobSpecScheduling(t *testing.T) {
	cfg := kubeFromYAML(t, `
node_selector: {workload: batch}
tolerations:
  - {key: dedicated, operator: Equal, value: batch, effect: NoSchedule}
  - {key: spot, operator: Exists, effect: NoExecute, tolerationSeconds: 60}
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - {key: kubernetes.io/arch, operator: In, values: [arm64]}
priority_class_name: batch-low
pod_labels: {team: platform, codereview/job: hijack}
pod_annotations: {karpenter.sh/do-not-disrupt: "true"}
`)
	k, err := NewKubernetesWithClient(cfg, nil, fake.NewClientset())
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"codereview/job": "crb-x"}
	spec := k.jobSpec("crb-x", "ns", labels, nil, context.Background()).Spec.Template
	if spec.Spec.NodeSelector["workload"] != "batch" {
		t.Errorf("nodeSelector = %v", spec.Spec.NodeSelector)
	}
	tol := spec.Spec.Tolerations
	if len(tol) != 2 || tol[0].Effect != corev1.TaintEffectNoSchedule || tol[1].TolerationSeconds == nil || *tol[1].TolerationSeconds != 60 {
		t.Errorf("tolerations = %+v", tol)
	}
	a := spec.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values[0] != "arm64" {
		t.Errorf("affinity = %+v", a)
	}
	if spec.Spec.PriorityClassName != "batch-low" {
		t.Errorf("priorityClassName = %q", spec.Spec.PriorityClassName)
	}
	if spec.Labels["team"] != "platform" || spec.Labels["codereview/job"] != "crb-x" || spec.Labels["codereview/role"] != "review" {
		t.Errorf("pod labels = %v (built-in labels must win)", spec.Labels)
	}
	if spec.Annotations["karpenter.sh/do-not-disrupt"] != "true" {
		t.Errorf("pod annotations = %v", spec.Annotations)
	}
}

func TestSchedulingTypoFails(t *testing.T) {
	for name, body := range map[string]string{
		"toleration": "tolerations: [{key: a, efect: NoSchedule}]\n",
		"affinity":   "affinity: {nodeAfinity: {}}\n",
	} {
		if _, err := NewKubernetesWithClient(kubeFromYAML(t, body), nil, fake.NewClientset()); err == nil {
			t.Errorf("%s: want an unknown-field error", name)
		}
	}
}

func TestJobSpecCABundle(t *testing.T) {
	for _, b := range []config.KubeCABundle{{ConfigMap: "corp-ca", Key: "bundle.pem"}, {Secret: "corp-ca", Key: "ca.crt"}} {
		k, err := NewKubernetesWithClient(config.KubeExecutor{CABundle: b}, nil, fake.NewClientset())
		if err != nil {
			t.Fatal(err)
		}
		pod := k.jobSpec("crb-x", "ns", nil, nil, context.Background()).Spec.Template.Spec
		c := pod.Containers[0]
		if len(c.Env) != 1 || c.Env[0].Name != "SSL_CERT_DIR" || c.Env[0].Value != caDir {
			t.Errorf("%+v: env = %+v", b, c.Env)
		}
		var mounted bool
		for _, m := range c.VolumeMounts {
			mounted = mounted || (m.Name == "ca-bundle" && m.MountPath == caDir && m.ReadOnly)
		}
		if !mounted {
			t.Errorf("%+v: mounts = %+v", b, c.VolumeMounts)
		}
		v := pod.Volumes[len(pod.Volumes)-1]
		switch {
		case b.ConfigMap != "":
			if v.ConfigMap == nil || v.ConfigMap.Name != "corp-ca" || v.ConfigMap.Items[0].Key != "bundle.pem" || v.ConfigMap.Items[0].Path != "ca.crt" {
				t.Errorf("volume = %+v", v)
			}
		default:
			if v.Secret == nil || v.Secret.SecretName != "corp-ca" || v.Secret.Items[0].Key != "ca.crt" {
				t.Errorf("volume = %+v", v)
			}
		}
	}
	// No bundle: no extra volume or env.
	k, _ := NewKubernetesWithClient(config.KubeExecutor{}, nil, fake.NewClientset())
	pod := k.jobSpec("crb-x", "ns", nil, nil, context.Background()).Spec.Template.Spec
	if len(pod.Volumes) != 1 || len(pod.Containers[0].Env) != 0 {
		t.Errorf("without ca_bundle: volumes %+v env %+v", pod.Volumes, pod.Containers[0].Env)
	}
}

// TestCreateFailedReportedAtOnce: a pod a ResourceQuota rejects is never
// created; the Job's FailedCreate event is reported instead of waiting
// for the deadline (issue #45).
func TestCreateFailedReportedAtOnce(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(&corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "crb-x.1", Namespace: "ns"},
		InvolvedObject: corev1.ObjectReference{Kind: "Job", Name: "crb-x", Namespace: "ns"},
		Reason:         "FailedCreate",
		Message:        `pods "crb-x-abc" is forbidden: failed quota: code-review: must specify requests.cpu for: codereview`,
	})
	k, _ := NewKubernetesWithClient(config.KubeExecutor{Namespace: "ns"}, nil, cs)
	if err := k.checkPodStuck(ctx, "crb-x"); err == nil || !strings.Contains(err.Error(), "failed quota") {
		t.Errorf("want the quota error, got %v", err)
	}
	if err := k.checkPodStuck(ctx, "crb-other"); err != nil {
		t.Errorf("another job's event must not count: %v", err)
	}
}

func TestCreateFailedWithoutEventsPermission(t *testing.T) {
	cs := fake.NewClientset()
	calls := 0
	cs.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("no"))
	})
	k, _ := NewKubernetesWithClient(config.KubeExecutor{Namespace: "ns"}, nil, cs)
	for i := 0; i < 3; i++ {
		if err := k.checkPodStuck(context.Background(), "crb-x"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("a forbidden events list is asked once, then skipped: %d calls", calls)
	}
}

// TestJobSpecStorageLimits: review pods get an ephemeral-storage request
// and limit and a /tmp size limit, by default (issue #30).
func TestJobSpecStorageLimits(t *testing.T) {
	spec := func(cfg config.KubeExecutor) corev1.PodSpec {
		k, err := NewKubernetesWithClient(cfg, nil, fake.NewClientset())
		if err != nil {
			t.Fatal(err)
		}
		return k.jobSpec("crb-x", "ns", map[string]string{}, nil, context.Background()).Spec.Template.Spec
	}
	check := func(name string, p corev1.PodSpec, eph, tmp string) {
		t.Helper()
		res := p.Containers[0].Resources
		gotEph, gotTmp := "", ""
		if q, ok := res.Limits[corev1.ResourceEphemeralStorage]; ok {
			gotEph = q.String()
			if r := res.Requests[corev1.ResourceEphemeralStorage]; r.String() != gotEph {
				t.Errorf("%s: request %s != limit %s", name, r.String(), gotEph)
			}
		}
		if sl := p.Volumes[0].EmptyDir.SizeLimit; sl != nil {
			gotTmp = sl.String()
		}
		if gotEph != eph || gotTmp != tmp {
			t.Errorf("%s: ephemeral-storage %q, /tmp %q; want %q, %q", name, gotEph, gotTmp, eph, tmp)
		}
	}
	check("defaults", spec(config.KubeExecutor{}), "2Gi", "1Gi")
	check("custom", spec(config.KubeExecutor{EphemeralStorage: "4Gi", TmpSizeLimit: "3Gi"}), "4Gi", "3Gi")
	check("none", spec(config.KubeExecutor{EphemeralStorage: "none", TmpSizeLimit: "none"}), "", "")

	for name, c := range map[string]config.KubeExecutor{
		"bad size":         {EphemeralStorage: "lots"},
		"tmp over storage": {EphemeralStorage: "1Gi", TmpSizeLimit: "2Gi"},
		"tmp over default": {TmpSizeLimit: "5Gi"},
	} {
		if err := CheckKube(c); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if err := CheckKube(config.KubeExecutor{EphemeralStorage: "none", TmpSizeLimit: "5Gi"}); err != nil {
		t.Errorf("no storage limit allows any /tmp size: %v", err)
	}
}
