package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// poll is how often job status is checked.
	poll time.Duration
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
	return &Kubernetes{cfg: cfg, pass: pass, cs: cs, poll: 2 * time.Second}, nil
}

// NewKubernetesWithClient is for tests.
func NewKubernetesWithClient(cfg config.KubeExecutor, pass []string, cs kubernetes.Interface) *Kubernetes {
	return &Kubernetes{cfg: cfg, pass: pass, cs: cs, poll: 50 * time.Millisecond}
}

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
	sec.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID}}
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		// Non-fatal: the deferred delete still removes it.
		_ = err
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
	res, err := ParseOutput(t.Kind, []byte(stdout))
	if err != nil {
		return nil, &RunError{Message: err.Error(), Logs: tail(stderr, 16<<10)}
	}
	res.Logs = tail(stderr, 16<<10)
	return res, nil
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
	automount := k.cfg.ServiceAccount != ""
	res := corev1.ResourceRequirements{Limits: corev1.ResourceList{}, Requests: corev1.ResourceList{}}
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
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
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
						EnvFrom:         []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
						Resources:       res,
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   &yes,
							AllowPrivilegeEscalation: &no,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
					}},
					Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
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
	if err != nil || len(pods.Items) == 0 {
		return nil
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
