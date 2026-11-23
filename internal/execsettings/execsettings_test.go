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
	d := "sha256:" + strings.Repeat("a", 64)
	for img, want := range map[string][2]string{
		"ghcr.io/divmora/code-review-bot:0.5.0":      {"ghcr.io/divmora/code-review-bot", "0.5.0"},
		"ghcr.io/divmora/code-review-bot:0.4.0@" + d: {"ghcr.io/divmora/code-review-bot", "0.4.0@" + d},
		"ghcr.io/divmora/code-review-bot@" + d:       {"ghcr.io/divmora/code-review-bot", d},
		"registry.example.com:5000/crb":              {"registry.example.com:5000/crb", ""},
		"registry.example.com:5000/crb:1.0":          {"registry.example.com:5000/crb", "1.0"},
		"registry.example.com:5000/crb:1.0@" + d:     {"registry.example.com:5000/crb", "1.0@" + d},
	} {
		r, v := SplitImage(img)
		if r != want[0] || v != want[1] {
			t.Errorf("%s: %s %s", img, r, v)
			continue
		}
		if back := joinImage(r, v); back != img {
			t.Errorf("%s: joins back as %s", img, back)
		}
	}
}

// TestPanelVersionOnPinnedImages changes the version from the panel for
// each way an image can be pinned in the config file (issue #41).
func TestPanelVersionOnPinnedImages(t *testing.T) {
	ctx := context.Background()
	d := "sha256:" + strings.Repeat("a", 64)
	d2 := "sha256:" + strings.Repeat("b", 64)
	for _, img := range []string{
		"ghcr.io/divmora/code-review-bot:0.4.0@" + d,
		"ghcr.io/divmora/code-review-bot@" + d,
		"registry.example.com:5000/crb:0.4.0",
		"registry.example.com:5000/crb:0.4.0@" + d,
	} {
		e := config.Executor{Type: "kubernetes", Kubernetes: config.KubeExecutor{Image: img, Namespace: "ns", TTL: time.Minute}}
		repo, _ := SplitImage(img)
		for version, want := range map[string]string{
			"0.5.0":       repo + ":0.5.0",
			d2:            repo + "@" + d2,
			"0.5.0@" + d2: repo + ":0.5.0@" + d2,
		} {
			s := New(e, storetest.New())
			_, _, _ = s.Seed(ctx)
			cur, _, _ := s.Get(ctx)
			cur.Version = version
			if err := s.Put(ctx, cur, "a@x"); err != nil {
				t.Errorf("%s -> %s: %v", img, version, err)
				continue
			}
			if eff, _ := s.Effective(ctx); eff.Kubernetes.Image != want {
				t.Errorf("%s -> %s: image %s, want %s", img, version, eff.Kubernetes.Image, want)
			}
		}
	}
}

// TestSeedFixesDigestOnlyVersion: 0.6.0 stored only the digest of a
// repo:tag@digest image; the next start keeps the tag with it.
func TestSeedFixesDigestOnlyVersion(t *testing.T) {
	ctx := context.Background()
	d := "sha256:" + strings.Repeat("a", 64)
	st := storetest.New()
	e := config.Executor{Type: "docker", Docker: config.DockerExecutor{Image: "ghcr.io/divmora/code-review-bot:0.4.0@" + d}}
	_ = st.PutSetting(ctx, Key, Settings{Type: "docker", Version: d}, SeedActor)
	s := New(e, st)
	seeded, differs, err := s.Seed(ctx)
	if err != nil || seeded || len(differs) != 0 {
		t.Fatalf("%v %v %v", seeded, differs, err)
	}
	if got, _, _ := s.Get(ctx); got.Version != "0.4.0@"+d {
		t.Errorf("version: %s", got.Version)
	}
	if eff, _ := s.Effective(ctx); eff.Docker.Image != e.Docker.Image {
		t.Errorf("image: %s", eff.Docker.Image)
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

// Issue #128: from the panel, the CA bundle is a ConfigMap or the Secret
// the config file names (with its key), never another Secret of the
// namespace, such as the console's own.
func TestCABundleSecret(t *testing.T) {
	ctx := context.Background()
	cfgFile := kube()
	cfgFile.Kubernetes.CABundle = config.KubeCABundle{Secret: "corp-ca", Key: "ca.crt"}
	s := New(cfgFile, storetest.New())
	base := FromConfig(cfgFile)
	for name, c := range map[string]struct {
		b  CABundle
		ok bool
	}{
		"the console's own Secret":     {CABundle{Secret: "code-review-console", Key: "CODEREVIEW_CONSOLE_SECRET_KEY"}, false},
		"another Secret":               {CABundle{Secret: "other"}, false},
		"the config file's, other key": {CABundle{Secret: "corp-ca", Key: "tls.key"}, false},
		"the config file's Secret":     {CABundle{Secret: "corp-ca", Key: "ca.crt"}, true},
		"default key":                  {CABundle{Secret: "corp-ca"}, true},
		"a ConfigMap":                  {CABundle{ConfigMap: "corp-ca-certs", Key: "ca.crt"}, true},
		"none":                         {CABundle{}, true},
	} {
		x := base
		x.CABundle = c.b
		err := s.Put(ctx, x, "a@x")
		if (err == nil) != c.ok || (err != nil && !strings.Contains(err.Error(), "ConfigMap")) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a Secret in the config file, the panel can't name any.
	if err := New(kube(), storetest.New()).Put(ctx, func() Settings { x := FromConfig(kube()); x.CABundle = CABundle{Secret: "corp-ca"}; return x }(), "a@x"); err == nil {
		t.Error("no Secret in the config file: none allowed")
	}

	// Saved before the rule: not mounted, and named for a warning.
	saved := base
	saved.CABundle = CABundle{Secret: "code-review-console", Key: "DATABASE_URL"}
	if e := Apply(cfgFile, saved); e.Kubernetes.CABundle.Enabled() {
		t.Errorf("a disallowed saved Secret isn't applied: %+v", e.Kubernetes.CABundle)
	}
	if IgnoredCASecret(cfgFile, saved) != "code-review-console" || IgnoredCASecret(cfgFile, base) != "" {
		t.Error("IgnoredCASecret")
	}
	if e := Apply(cfgFile, base); e.Kubernetes.CABundle.Secret != "corp-ca" {
		t.Errorf("the config file's Secret still applies: %+v", e.Kubernetes.CABundle)
	}
}
