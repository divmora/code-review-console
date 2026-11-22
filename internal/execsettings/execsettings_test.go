package execsettings

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func kube() config.Executor {
	return config.Executor{Type: "kubernetes", Kubernetes: config.KubeExecutor{
		Image: "registry.example.com:5000/divmora/code-review-bot:0.5.0", Namespace: "reviews", ServiceAccount: "crb",
		CPU: "500m", Memory: "1Gi", TTL: 10 * time.Minute, NodeSelector: map[string]string{"pool": "batch"},
	}}
}

func TestSplitImage(t *testing.T) {
	for img, want := range map[string][2]string{
		"ghcr.io/divmora/code-review-bot:0.5.0": {"ghcr.io/divmora/code-review-bot", "0.5.0"},
		"registry.example.com:5000/crb":         {"registry.example.com:5000/crb", ""},
		"registry.example.com:5000/crb:1.0":     {"registry.example.com:5000/crb", "1.0"},
		"crb@sha256:" + strings.Repeat("a", 64): {"crb", "sha256:" + strings.Repeat("a", 64)},
	} {
		if r, v := SplitImage(img); r != want[0] || v != want[1] {
			t.Errorf("%s: %s %s", img, r, v)
		}
	}
	if got := joinImage("crb", "sha256:"+strings.Repeat("a", 64)); got != "crb@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("digest join: %s", got)
	}
}

func TestSeedPutAndApply(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := New(kube(), st)
	seeded, _, err := s.Seed(ctx)
	if err != nil || !seeded {
		t.Fatalf("seed: %v %v", seeded, err)
	}
	got, found, _ := s.Get(ctx)
	if !found || got.Version != "0.5.0" || got.CPU != "500m" || got.TTL != "10m" || got.NodeSelector["pool"] != "batch" {
		t.Fatalf("seeded: %+v", got)
	}
	// A second start: nothing seeded, nothing differs.
	if seeded, differs, _ := s.Seed(ctx); seeded || len(differs) != 0 {
		t.Errorf("second seed: %v %v", seeded, differs)
	}

	got.Version, got.Memory, got.PriorityClassName = "0.6.0", "2Gi", "batch-low"
	got.Tolerations = []map[string]any{{"key": "spot", "operator": "Exists"}}
	if err := s.Put(ctx, got, "a@x"); err != nil {
		t.Fatal(err)
	}
	eff, _ := s.Effective(ctx)
	k := eff.Kubernetes
	if k.Image != "registry.example.com:5000/divmora/code-review-bot:0.6.0" || k.Memory != "2Gi" || k.PriorityClassName != "batch-low" || len(k.Tolerations) != 1 ||
		k.Namespace != "reviews" || k.ServiceAccount != "crb" {
		t.Errorf("effective: %+v", k)
	}
	// The config file now differs from the database: reported, database wins.
	if _, differs, _ := s.Seed(ctx); strings.Join(differs, ",") != "version,memory" {
		t.Errorf("differs: %v", differs)
	}

	// The config file switches executors: the saved settings don't apply.
	d := New(config.Executor{Type: "docker", Docker: config.DockerExecutor{Image: "crb:0.5.0", CPUs: "1"}}, st)
	if _, found, _ := d.Get(ctx); found {
		t.Error("kubernetes settings must not apply to docker")
	}
	if seeded, _, _ := d.Seed(ctx); !seeded {
		t.Error("docker settings are seeded after the switch")
	}
}

func TestValidate(t *testing.T) {
	ctx := context.Background()
	s := New(kube(), storetest.New())
	base := FromConfig(kube())
	for name, c := range map[string]struct {
		mod  func(*Settings)
		want string
	}{
		"no version":  {func(x *Settings) { x.Version = "" }, "version is required"},
		"repo in tag": {func(x *Settings) { x.Version = "evil.example.com/x:1" }, "image tag"},
		"old cli":     {func(x *Settings) { x.Version = "0.1.0" }, "or later"},
		"cpu":         {func(x *Settings) { x.CPU = "lots" }, "quantity"},
		"ttl":         {func(x *Settings) { x.TTL = "5s" }, "ttl"},
		"toleration":  {func(x *Settings) { x.Tolerations = []map[string]any{{"efect": "NoSchedule"}} }, "unknown field"},
		"label":       {func(x *Settings) { x.PodLabels = map[string]string{"bad key!": "x"} }, "pod_labels"},
		"priority":    {func(x *Settings) { x.PriorityClassName = "Not_DNS" }, "priority_class_name"},
		"both ca":     {func(x *Settings) { x.CABundle = CABundle{ConfigMap: "a", Secret: "b"} }, "not both"},
	} {
		x := base
		c.mod(&x)
		if err := s.Put(ctx, x, "a@x"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	x := base
	x.Version = "sha256:" + strings.Repeat("b", 64)
	if err := s.Put(ctx, x, "a@x"); err != nil {
		t.Errorf("a digest is a version: %v", err)
	}
	d := New(config.Executor{Type: "docker", Docker: config.DockerExecutor{Image: "crb:0.5.0"}}, storetest.New())
	if err := d.Put(ctx, Settings{Version: "0.5.0", CPU: "1.5", Memory: "lots"}, "a@x"); err == nil {
		t.Error("docker memory must be a size")
	}
	if err := New(config.Executor{Type: "local"}, storetest.New()).Put(ctx, Settings{}, "a@x"); err == nil {
		t.Error("the local executor has no panel settings")
	}
}

type fake struct{ image string }

func (f *fake) Name() string { return "fake" }
func (f *fake) Run(context.Context, executor.Task) (*executor.Result, error) {
	return &executor.Result{SkipReason: f.image}, nil
}

func TestExecutorAppliesChanges(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := New(kube(), st)
	_, _, _ = s.Seed(ctx)
	builds := 0
	e := NewExecutor(s, &fake{image: "base"}, func(c config.Executor) (executor.Executor, error) {
		builds++
		return &fake{image: c.Kubernetes.Image}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r, _ := e.Run(ctx, executor.Task{})
	if r.SkipReason != "base" || builds != 0 {
		t.Fatalf("unchanged settings use the startup executor: %s %d", r.SkipReason, builds)
	}
	cur, _, _ := s.Get(ctx)
	cur.Version = "0.6.0"
	if err := s.Put(ctx, cur, "a@x"); err != nil {
		t.Fatal(err)
	}
	r, _ = e.Run(ctx, executor.Task{})
	r2, _ := e.Run(ctx, executor.Task{})
	if !strings.HasSuffix(r.SkipReason, ":0.6.0") || r2.SkipReason != r.SkipReason || builds != 1 {
		t.Errorf("the next review uses the new version, built once: %s %s %d", r.SkipReason, r2.SkipReason, builds)
	}
}
