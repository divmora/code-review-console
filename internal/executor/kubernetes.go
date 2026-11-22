package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/divmora/code-review-console/internal/config"
)

// Kubernetes runs each task as a Job. The job's environment (config,
// overrides, tokens, provider keys) goes into a Secret that is deleted as
// soon as the result is read.
type Kubernetes struct {
	cfg  config.KubeExecutor
	pass []string
	cs   kubernetes.Interface
	// tolerations and affinity are parsed from cfg once, at construction.
	tolerations []corev1.Toleration
	affinity    *corev1.Affinity
	// poll is how often job status is checked.
	poll time.Duration
	// noEvents is set once listing events is forbidden.
	noEvents *atomic.Bool
}

// NewKubernetes connects using the kubeconfig path, or in-cluster config.
func NewKubernetes(cfg config.KubeExecutor, pass []string) (*Kubernetes, error) {
	var rc *rest.Config
	var err error
	if cfg.Kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes executor: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("kubernetes executor: %w", err)
	}
	return newKubernetes(cfg, pass, cs, 2*time.Second)
}

// NewKubernetesWithClient is for tests.
func NewKubernetesWithClient(cfg config.KubeExecutor, pass []string, cs kubernetes.Interface) (*Kubernetes, error) {
	return newKubernetes(cfg, pass, cs, 50*time.Millisecond)
}

// With returns a copy that uses cfg (changed in the admin panel) and the
// same Kubernetes client. The connection settings (kubeconfig, namespace)
// are the caller's to keep.
func (k *Kubernetes) With(cfg config.KubeExecutor) (*Kubernetes, error) {
	return newKubernetes(cfg, k.pass, k.cs, k.poll)
}

// Storage defaults for review pods (issue #30): enough for a review, small
// enough that a runaway one can't fill a node's disk.
const (
	DefaultEphemeralStorage = "2Gi"
	DefaultTmpSizeLimit     = "1Gi"
)

// storageQuantity reads a storage setting: "" is the default, "none" no
// limit.
func storageQuantity(v, def string) (resource.Quantity, bool) {
	switch strings.TrimSpace(v) {
	case "none":
		return resource.Quantity{}, false
	case "":
		v = def
	}
	q, err := resource.ParseQuantity(v)
	return q, err == nil
}

// CheckKube checks the pod settings of cfg the way the executor reads them:
// tolerations and affinity as Kubernetes types (unknown fields rejected),
// label and annotation keys, label values, the priority class name and
// resource quantities.
func CheckKube(cfg config.KubeExecutor) error {
	if _, err := newKubernetes(cfg, nil, nil, 0); err != nil {
		return err
	}
	for _, q := range []struct{ name, v string }{{"ephemeral_storage", cfg.EphemeralStorage}, {"tmp_size_limit", cfg.TmpSizeLimit}} {
		if q.v != "" && q.v != "none" {
			if _, err := resource.ParseQuantity(q.v); err != nil {
				return fmt.Errorf("%s %q: want a size such as 1Gi or 500Mi, or none", q.name, q.v)
			}
		}
	}
	if e, t := cfg.EphemeralStorage, cfg.TmpSizeLimit; e != "none" && t != "none" {
		eq, _ := storageQuantity(e, DefaultEphemeralStorage)
		tq, _ := storageQuantity(t, DefaultTmpSizeLimit)
		if tq.Cmp(eq) > 0 {
			return fmt.Errorf("tmp_size_limit (%s) can't be more than ephemeral_storage (%s): /tmp counts towards it", tq.String(), eq.String())
		}
	}
	for _, q := range []struct{ name, v string }{{"cpu", cfg.CPU}, {"memory", cfg.Memory}} {
		if q.v != "" {
			if _, err := resource.ParseQuantity(q.v); err != nil {
				return fmt.Errorf("%s %q: want a Kubernetes quantity such as 500m or 1Gi", q.name, q.v)
			}
		}
	}
	for k, v := range cfg.NodeSelector {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("node_selector key %q: %s", k, errs[0])
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return fmt.Errorf("node_selector %s value %q: %s", k, v, errs[0])
		}
	}
	for k, v := range cfg.PodLabels {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("pod_labels key %q: %s", k, errs[0])
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return fmt.Errorf("pod_labels %s value %q: %s", k, v, errs[0])
		}
	}
	for k := range cfg.PodAnnotations {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("pod_annotations key %q: %s", k, errs[0])
		}
	}
	if p := cfg.PriorityClassName; p != "" {
		if errs := validation.IsDNS1123Subdomain(p); len(errs) > 0 {
			return fmt.Errorf("priority_class_name %q: %s", p, errs[0])
		}
	}
	b := cfg.CABundle
	if b.ConfigMap != "" && b.Secret != "" {
		return fmt.Errorf("ca_bundle: set config_map or secret, not both")
	}
	for _, n := range []string{b.ConfigMap, b.Secret} {
		if n != "" {
			if errs := validation.IsDNS1123Subdomain(n); len(errs) > 0 {
				return fmt.Errorf("ca_bundle name %q: %s", n, errs[0])
			}
		}
	}
	if b.Key != "" {
		if errs := validation.IsConfigMapKey(b.Key); len(errs) > 0 {
			return fmt.Errorf("ca_bundle key %q: %s", b.Key, errs[0])
		}
	}
	return nil
}

func newKubernetes(cfg config.KubeExecutor, pass []string, cs kubernetes.Interface, poll time.Duration) (*Kubernetes, error) {
	k := &Kubernetes{cfg: cfg, pass: pass, cs: cs, poll: poll, noEvents: &atomic.Bool{}}
	if len(cfg.Tolerations) > 0 {
		if err := strictJSON(cfg.Tolerations, &k.tolerations); err != nil {
			return nil, fmt.Errorf("executor.kubernetes.tolerations: %w", err)
		}
	}
	if len(cfg.Affinity) > 0 {
		k.affinity = &corev1.Affinity{}
		if err := strictJSON(cfg.Affinity, k.affinity); err != nil {
			return nil, fmt.Errorf("executor.kubernetes.affinity: %w", err)
		}
	}
	return k, nil
}

// strictJSON converts a YAML-decoded value into a Kubernetes API type,
// rejecting unknown fields so a typo fails at startup, not silently.
func strictJSON(in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// caDir is where the CA bundle is mounted in review pods. SSL_CERT_DIR
// points at it: Go then still loads the image's system bundle file and adds
// the certificates in this directory.
const caDir = "/etc/codereview/ca"

func (k *Kubernetes) Name() string { return "kubernetes" }

var dnsUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// jobName returns a valid, unique DNS label (max 63 chars; pods add a suffix).
func jobName(id string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	n := strings.Trim(dnsUnsafe.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-")
	}
	return "crb-" + n + "-" + hex.EncodeToString(b)
}

func (k *Kubernetes) Run(ctx context.Context, t Task) (*Result, error) {
	args, err := Args(t)
	if err != nil {
		return nil, err
	}
	env, err := Env(t)
	if err != nil {
		return nil, err
	}
	for _, n := range k.pass {
		if v, ok := os.LookupEnv(n); ok {
			if _, set := env[n]; !set {
				env[n] = v
			}
		}
	}
	mb := make([]byte, 12)
	_, _ = rand.Read(mb)
	marker := "@@CODEREVIEW-RESULT-" + hex.EncodeToString(mb) + "@@"
	env["CODEREVIEW_RESULT_MARKER"] = marker

	name := jobName(t.ID)
	ns := k.cfg.Namespace
	labels := map[string]string{"app.kubernetes.io/name": "codereview", "app.kubernetes.io/managed-by": "code-review-console", "codereview/job": name}
	data := map[string][]byte{}
	for key, v := range env {
		data[key] = []byte(v)
	}
	secrets := k.cs.CoreV1().Secrets(ns)
	sec, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create job secret: %w", err)
	}
	cleanupSecret := func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = secrets.Delete(c, sec.Name, metav1.DeleteOptions{})
	}
	defer cleanupSecret()

	job := k.jobSpec(name, ns, labels, args, ctx)
	jobs := k.cs.BatchV1().Jobs(ns)
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	// Garbage-collect the secret with the job if the console dies mid-run.
	if err := k.ownSecret(ctx, sec.Name, created); err != nil {
		// Non-fatal: the deferred delete still removes it on the normal path.
		slog.Warn("kubernetes executor: could not make the job secret owned by its job; "+
			"it will be left behind if the console stops mid-review (does the Role allow patch on secrets?)",
			"secret", sec.Name, "namespace", ns, "error", err)
	}

	succeeded, err := k.wait(ctx, name)
	if err != nil {
		bg := metav1.DeletePropagationBackground
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_ = jobs.Delete(c, name, metav1.DeleteOptions{PropagationPolicy: &bg})
		cancel()
		return nil, err
	}
	logs, exitCode, err := k.podOutput(ctx, name)
	if err != nil {
		return nil, err
	}
	stdout, stderr := splitMarker(logs, marker)
	if !succeeded {
		return nil, &RunError{ExitCode: exitCode, Message: lastErrorLine(stderr), Logs: tail(stderr, 16<<10)}
	}
	res, removed, err := parseMerged(t.Kind, stdout)
	if len(removed) > 0 { // log lines that were spliced into the result
		stderr = strings.TrimRight(stderr, "\n") + "\n" + strings.Join(removed, "\n") + "\n"
	}
	if err != nil {
		return nil, &RunError{Message: err.Error(), Logs: tail(stderr, 16<<10)}
	}
	res.Logs = tail(stderr, 16<<10)
	return res, nil
}

// ownSecret adds an ownerReference from the job secret to its Job, so the
// secret is garbage-collected with the Job. It needs "patch" on secrets.
func (k *Kubernetes) ownSecret(ctx context.Context, secret string, job *batchv1.Job) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"ownerReferences": []metav1.OwnerReference{{
		APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
	}}}})
	if err != nil {
		return err
	}
	_, err = k.cs.CoreV1().Secrets(job.Namespace).Patch(ctx, secret, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (k *Kubernetes) jobSpec(name, ns string, labels map[string]string, args []string, ctx context.Context) *batchv1.Job {
	backoff := int32(0)
	ttl := int32(k.cfg.TTL.Seconds())
	var deadline *int64
	if d, ok := ctx.Deadline(); ok {
		secs := int64(time.Until(d).Seconds())
		if secs < 30 {
			secs = 30
		}
		deadline = &secs
	}
	yes, no := true, false
	uid := int64(10001)
	// Review pods run untrusted PR content, so they get no Kubernetes API
	// token unless explicitly asked for. Cloud identities (IRSA, EKS Pod
	// Identity, GKE Workload Identity) don't need it.
	automount := k.cfg.AutomountServiceAccountToken
	res := corev1.ResourceRequirements{Limits: corev1.ResourceList{}, Requests: corev1.ResourceList{}}
	if q, ok := storageQuantity(k.cfg.EphemeralStorage, DefaultEphemeralStorage); ok {
		res.Limits[corev1.ResourceEphemeralStorage] = q
		res.Requests[corev1.ResourceEphemeralStorage] = q
	}
	if k.cfg.CPU != "" {
		if q, err := resource.ParseQuantity(k.cfg.CPU); err == nil {
			res.Limits[corev1.ResourceCPU] = q
			res.Requests[corev1.ResourceCPU] = q
		}
	}
	if k.cfg.Memory != "" {
		if q, err := resource.ParseQuantity(k.cfg.Memory); err == nil {
			res.Limits[corev1.ResourceMemory] = q
			res.Requests[corev1.ResourceMemory] = q
		}
	}
	podLabels := map[string]string{}
	for key, v := range k.cfg.PodLabels {
		podLabels[key] = v
	}
	for key, v := range labels { // built-in labels win: pods are found by codereview/job
		podLabels[key] = v
	}
	var annotations map[string]string
	if len(k.cfg.PodAnnotations) > 0 {
		annotations = map[string]string{}
		for key, v := range k.cfg.PodAnnotations {
			annotations[key] = v
		}
	}
	mounts := []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}
	tmp := &corev1.EmptyDirVolumeSource{}
	if q, ok := storageQuantity(k.cfg.TmpSizeLimit, DefaultTmpSizeLimit); ok {
		tmp.SizeLimit = &q
	}
	volumes := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: tmp}}}
	var env []corev1.EnvVar
	if b := k.cfg.CABundle; b.Enabled() {
		items := []corev1.KeyToPath{{Key: b.Key, Path: "ca.crt"}}
		src := corev1.VolumeSource{}
		if b.ConfigMap != "" {
			src.ConfigMap = &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: b.ConfigMap}, Items: items}
		} else {
			src.Secret = &corev1.SecretVolumeSource{SecretName: b.Secret, Items: items}
		}
		volumes = append(volumes, corev1.Volume{Name: "ca-bundle", VolumeSource: src})
		mounts = append(mounts, corev1.VolumeMount{Name: "ca-bundle", MountPath: caDir, ReadOnly: true})
		env = append(env, corev1.EnvVar{Name: "SSL_CERT_DIR", Value: caDir})
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: annotations},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					NodeSelector:                 k.cfg.NodeSelector,
					Tolerations:                  k.tolerations,
					Affinity:                     k.affinity,
					PriorityClassName:            k.cfg.PriorityClassName,
					ServiceAccountName:           k.cfg.ServiceAccount,
					AutomountServiceAccountToken: &automount,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &yes,
						RunAsUser:      &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "codereview",
						Image:           k.cfg.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args:            args,
						Env:             env,
						EnvFrom:         []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
						Resources:       res,
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   &yes,
							AllowPrivilegeEscalation: &no,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: mounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// wait polls until the job finishes. It returns whether it succeeded.
func (k *Kubernetes) wait(ctx context.Context, name string) (bool, error) {
	jobs := k.cs.BatchV1().Jobs(k.cfg.Namespace)
	tick := time.NewTicker(k.poll)
	defer tick.Stop()
	for {
		j, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, fmt.Errorf("get job: %w", err)
		}
		if j.Status.Succeeded > 0 {
			return true, nil
		}
		if j.Status.Failed > 0 {
			return false, nil
		}
		for _, c := range j.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				return false, nil
			}
		}
		if err := k.checkPodStuck(ctx, name); err != nil {
			return false, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-tick.C:
		}
	}
}

// checkPodStuck turns image-pull failures into errors instead of waiting
// for the deadline.
func (k *Kubernetes) checkPodStuck(ctx context.Context, name string) error {
	pods, err := k.cs.CoreV1().Pods(k.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "codereview/job=" + name})
	if err != nil {
		return nil
	}
	if len(pods.Items) == 0 {
		return k.checkCreateFailed(ctx, name)
	}
	for _, cs := range pods.Items[0].Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError":
				return fmt.Errorf("review pod cannot start: %s: %s", w.Reason, w.Message)
			}
		}
	}
	return nil
}

// checkCreateFailed reports a Job whose pod Kubernetes refuses to create
// (a ResourceQuota, a LimitRange, an admission webhook) instead of waiting
// for the deadline. It reads the Job's FailedCreate events, which needs
// "list" on events; without that permission it does nothing.
func (k *Kubernetes) checkCreateFailed(ctx context.Context, name string) error {
	if k.noEvents.Load() {
		return nil
	}
	evs, err := k.cs.CoreV1().Events(k.cfg.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Job,involvedObject.name=" + name + ",reason=FailedCreate",
	})
	if err != nil {
		if apierrors.IsForbidden(err) {
			k.noEvents.Store(true) // not allowed: don't ask again
		}
		return nil
	}
	for _, e := range evs.Items {
		if e.Reason == "FailedCreate" && e.InvolvedObject.Name == name {
			return fmt.Errorf("review pod cannot be created: %s", e.Message)
		}
	}
	return nil
}

func (k *Kubernetes) podOutput(ctx context.Context, name string) (string, int, error) {
	pods := k.cs.CoreV1().Pods(k.cfg.Namespace)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: "codereview/job=" + name})
	if err != nil {
		return "", -1, fmt.Errorf("list job pods: %w", err)
	}
	if len(list.Items) == 0 {
		return "", -1, errors.New("job finished without a pod (check the namespace's quotas and events)")
	}
	pod := list.Items[0]
	exit := -1
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			exit = int(cs.State.Terminated.ExitCode)
		}
	}
	limit := int64(32 << 20)
	stream, err := pods.GetLogs(pod.Name, &corev1.PodLogOptions{Container: "codereview", LimitBytes: &limit}).Stream(ctx)
	if err != nil {
		return "", exit, fmt.Errorf("read job logs: %w", err)
	}
	defer func() { _ = stream.Close() }()
	b, err := io.ReadAll(stream)
	if err != nil {
		return "", exit, fmt.Errorf("read job logs: %w", err)
	}
	return string(b), exit, nil
}

// splitMarker extracts the result the CLI printed between "<marker>" and
// "<marker>-END" (stdout), and returns everything else as logs. Logs can
// come before and after the result because pod logs merge both streams.
func splitMarker(logs, marker string) (stdout, stderr string) {
	endTag := marker + "-END"
	end := strings.LastIndex(logs, endTag)
	if end < 0 {
		i := strings.LastIndex(logs, marker)
		if i < 0 {
			return "", logs
		}
		return strings.TrimLeft(logs[i+len(marker):], "\r\n"), logs[:i]
	}
	start := strings.LastIndex(logs[:end], marker)
	if start < 0 {
		return "", logs
	}
	return strings.Trim(logs[start+len(marker):end], "\r\n") + "\n", logs[:start] + logs[end+len(endTag):]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
