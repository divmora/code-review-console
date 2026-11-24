// Package execsettings holds the executor settings managed in the admin
// panel: the review image's version, resources, and for Kubernetes the pod
// scheduling, metadata, CA bundle and job TTL. They apply to the next
// review, without a restart.
//
// What decides where reviews run and what they can reach stays in the
// config file: the executor type, binaries, the Docker network, the
// kubeconfig, namespace and service account, the API token automount, and
// the image repository (the panel picks only its tag, so a panel admin
// can't point reviews, which carry tokens and model keys, at another
// image).
package execsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/store"
)

// Key is the settings row holding Settings.
const Key = "executor"

// SeedActor is the audit actor of the copy from the config file.
const SeedActor = "config file (initial)"

// Settings are the executor settings the admin panel changes. Docker uses
// Version, CPU (docker --cpus) and Memory; Kubernetes uses all of them.
type Settings struct {
	// Type is the executor the settings are for. When the config file
	// switches executors, settings saved for the old one are not used.
	Type string `json:"type"`
	// Version is the image tag (0.5.0), digest (sha256:...) or both
	// (0.5.0@sha256:...).
	Version string `json:"version"`
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
	TTL     string `json:"ttl,omitempty"`
	// EphemeralStorage and TmpSizeLimit: "" = default, "none" = no limit.
	EphemeralStorage  string            `json:"ephemeral_storage,omitempty"`
	TmpSizeLimit      string            `json:"tmp_size_limit,omitempty"`
	NodeSelector      map[string]string `json:"node_selector,omitempty"`
	Tolerations       []map[string]any  `json:"tolerations,omitempty"`
	Affinity          map[string]any    `json:"affinity,omitempty"`
	PriorityClassName string            `json:"priority_class_name,omitempty"`
	PodLabels         map[string]string `json:"pod_labels,omitempty"`
	PodAnnotations    map[string]string `json:"pod_annotations,omitempty"`
	CABundle          CABundle          `json:"ca_bundle"`
}

// CABundle is config.KubeCABundle with JSON names.
type CABundle struct {
	ConfigMap string `json:"config_map,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Key       string `json:"key,omitempty"`
}

// SplitImage splits an image into its repository and version: the tag
// (0.5.0), the digest (sha256:...), both (0.5.0@sha256:...), or "".
// A registry port (host:5000/repo) is part of the repository.
func SplitImage(image string) (repo, version string) {
	ref, digest, _ := strings.Cut(image, "@")
	repo, tag := ref, ""
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	switch {
	case tag != "" && digest != "":
		return repo, tag + "@" + digest
	case digest != "":
		return repo, digest
	}
	return repo, tag
}

func joinImage(repo, version string) string {
	switch {
	case version == "":
		return repo
	case strings.HasPrefix(version, "sha256:"):
		return repo + "@" + version
	}
	return repo + ":" + version // a tag, or tag@digest
}

// image returns the configured image of the active executor.
func image(e config.Executor) string {
	if e.Type == "docker" {
		return e.Docker.Image
	}
	return e.Kubernetes.Image
}

// FromConfig reads the settings of the active executor from the config file.
func FromConfig(e config.Executor) Settings {
	_, v := SplitImage(image(e))
	switch e.Type {
	case "docker":
		return Settings{Type: e.Type, Version: v, CPU: e.Docker.CPUs, Memory: e.Docker.Memory}
	case "kubernetes":
		k := e.Kubernetes
		s := Settings{Type: e.Type, Version: v, CPU: k.CPU, Memory: k.Memory, EphemeralStorage: k.EphemeralStorage, TmpSizeLimit: k.TmpSizeLimit, NodeSelector: k.NodeSelector, Tolerations: k.Tolerations,
			Affinity: k.Affinity, PriorityClassName: k.PriorityClassName, PodLabels: k.PodLabels, PodAnnotations: k.PodAnnotations,
			CABundle: CABundle{ConfigMap: k.CABundle.ConfigMap, Secret: k.CABundle.Secret, Key: k.CABundle.Key}}
		if k.TTL > 0 {
			s.TTL = shortDuration(k.TTL)
		}
		return s
	}
	return Settings{}
}

// caSecretAllowed: the panel may use a Secret for the CA bundle only when
// it's the one the config file names, with the same key. A review pod
// processes untrusted code, and the namespace usually also holds the
// console's own Secret (CODEREVIEW_CONSOLE_SECRET_KEY, DATABASE_URL): any
// other Secret name would mount one of its keys into every review pod
// (issue #128). CA certificates aren't secret: use a ConfigMap.
func caSecretAllowed(e config.Executor, b CABundle) bool {
	if b.Secret == "" {
		return true
	}
	file := e.Kubernetes.CABundle
	key := firstNonEmptyStr(b.Key, "ca.crt")
	return b.Secret == file.Secret && key == firstNonEmptyStr(file.Key, "ca.crt")
}

// IgnoredCASecret names a saved CA bundle Secret that Apply leaves out
// (see caSecretAllowed), for a warning; "" when there is none.
func IgnoredCASecret(e config.Executor, s Settings) string {
	if e.Type != "kubernetes" || caSecretAllowed(e, s.CABundle) {
		return ""
	}
	return s.CABundle.Secret
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// Apply returns e with s in place of the config file's values.
func Apply(e config.Executor, s Settings) config.Executor {
	switch e.Type {
	case "docker":
		repo, _ := SplitImage(e.Docker.Image)
		e.Docker.Image, e.Docker.CPUs, e.Docker.Memory = joinImage(repo, s.Version), s.CPU, s.Memory
	case "kubernetes":
		k := &e.Kubernetes
		repo, _ := SplitImage(k.Image)
		k.Image, k.CPU, k.Memory = joinImage(repo, s.Version), s.CPU, s.Memory
		k.EphemeralStorage, k.TmpSizeLimit = s.EphemeralStorage, s.TmpSizeLimit
		if d, err := time.ParseDuration(s.TTL); err == nil && d > 0 {
			k.TTL = d
		}
		k.NodeSelector, k.Tolerations, k.Affinity = s.NodeSelector, s.Tolerations, s.Affinity
		k.PriorityClassName, k.PodLabels, k.PodAnnotations = s.PriorityClassName, s.PodLabels, s.PodAnnotations
		if caSecretAllowed(e, s.CABundle) {
			k.CABundle = config.KubeCABundle{ConfigMap: s.CABundle.ConfigMap, Secret: s.CABundle.Secret, Key: s.CABundle.Key}
		} else {
			k.CABundle = config.KubeCABundle{} // saved before this rule: not mounted (IgnoredCASecret warns)
		}
		if k.CABundle.Enabled() && k.CABundle.Key == "" {
			k.CABundle.Key = "ca.crt"
		}
	}
	return e
}

var (
	tagRe        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestRe     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	tagDigestRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}@sha256:[a-f0-9]{64}$`)
	dockerMemRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[bkmgBKMG]?$`)
	maxTTL       = 7 * 24 * time.Hour
	errNoChanges = errors.New("the local executor has no settings in the admin panel; it runs the codereview binary on the console host")
)

// PlainTag reports whether version is a tag alone (0.6.1), which the
// Executor page pins to its digest when it saves (issue #135).
func PlainTag(version string) bool { return tagRe.MatchString(version) }

// PinnedTag splits "0.6.1@sha256:…" into its tag and digest (ok false
// for a tag alone or a digest alone).
func PinnedTag(version string) (tag, digest string, ok bool) {
	if !tagDigestRe.MatchString(version) {
		return "", "", false
	}
	tag, digest, _ = strings.Cut(version, "@")
	return tag, digest, true
}

// ImageRepo is the repository of the image reviews run with ("" for the
// local executor).
func ImageRepo(e config.Executor) string {
	if e.Type != "docker" && e.Type != "kubernetes" {
		return ""
	}
	repo, _ := SplitImage(image(e))
	return repo
}

// Validate checks s for the executor e (the config file's, defaults applied).
func Validate(e config.Executor, s Settings) error {
	if e.Type != "docker" && e.Type != "kubernetes" {
		return errNoChanges
	}
	if s.Version == "" {
		return errors.New("version is required (an image tag such as 0.5.0, a sha256: digest, or both: 0.5.0@sha256:...)")
	}
	if !tagRe.MatchString(s.Version) && !digestRe.MatchString(s.Version) && !tagDigestRe.MatchString(s.Version) {
		return fmt.Errorf("version %q: want an image tag such as 0.5.0, sha256:<64 hex digits>, or both (0.5.0@sha256:...)", s.Version)
	}
	ex := Apply(e, s)
	if err := config.CheckImageTag("version", image(ex)); err != nil {
		return err
	}
	if e.Type == "docker" {
		if s.CPU != "" {
			if f, err := strconv.ParseFloat(s.CPU, 64); err != nil || f <= 0 {
				return fmt.Errorf("cpu %q: want a number of CPUs such as 1 or 1.5", s.CPU)
			}
		}
		if s.Memory != "" && !dockerMemRe.MatchString(s.Memory) {
			return fmt.Errorf("memory %q: want a size such as 512m or 2g", s.Memory)
		}
		return nil
	}
	if s.TTL != "" {
		d, err := time.ParseDuration(s.TTL)
		if err != nil || d < time.Minute || d > maxTTL {
			return fmt.Errorf("ttl %q: want a duration between 1m and 168h, such as 10m", s.TTL)
		}
	}
	if s.CABundle.ConfigMap != "" && s.CABundle.Secret != "" {
		return errors.New("ca_bundle: set config_map or secret, not both")
	}
	if err := executor.CheckKube(ex.Kubernetes); err != nil {
		return err
	}
	if !caSecretAllowed(e, s.CABundle) {
		return fmt.Errorf("ca_bundle: put the CA certificates in a ConfigMap (they aren't secret); a Secret can only be the one the config file names (executor.kubernetes.ca_bundle.secret), so no other Secret of the namespace, such as the console's own, is mounted into review pods")
	}
	return nil
}

// Service reads and changes the executor settings.
type Service struct {
	exec config.Executor // the config file's, defaults applied
	st   store.Store
}

// New returns a Service for the config file's executor (st may be nil:
// then the config file's settings are used as they are).
func New(e config.Executor, st store.Store) *Service {
	return &Service{exec: e, st: st}
}

// Type is the executor type.
func (s *Service) Type() string { return s.exec.Type }

// Config is the config file's executor.
func (s *Service) Config() config.Executor { return s.exec }

// Editable reports whether the panel can change settings.
func (s *Service) Editable() bool {
	return s.st != nil && (s.exec.Type == "docker" || s.exec.Type == "kubernetes")
}

// Get returns the settings in use and whether they come from the database.
func (s *Service) Get(ctx context.Context) (Settings, bool, error) {
	if !s.Editable() {
		return FromConfig(s.exec), false, nil
	}
	var st Settings
	found, err := s.st.GetSetting(ctx, Key, &st)
	if err != nil || !found || st.Type != s.exec.Type {
		return FromConfig(s.exec), false, err
	}
	return st, true, nil
}

// Effective returns the executor configuration reviews run with now.
func (s *Service) Effective(ctx context.Context) (config.Executor, error) {
	st, found, err := s.Get(ctx)
	if err != nil || !found {
		return s.exec, err
	}
	return Apply(s.exec, st), nil
}

// Put validates and stores settings.
func (s *Service) Put(ctx context.Context, st Settings, actor string) error {
	if !s.Editable() {
		if s.st == nil {
			return errors.New("executor settings need a database")
		}
		return errNoChanges
	}
	st = clean(st)
	st.Type = s.exec.Type
	if err := Validate(s.exec, st); err != nil {
		return err
	}
	return s.st.PutSetting(ctx, Key, st, actor)
}

// clean trims strings and drops empty maps, so equal settings compare equal.
func clean(st Settings) Settings {
	st.Version, st.CPU, st.Memory, st.TTL = strings.TrimSpace(st.Version), strings.TrimSpace(st.CPU), strings.TrimSpace(st.Memory), strings.TrimSpace(st.TTL)
	st.PriorityClassName = strings.TrimSpace(st.PriorityClassName)
	st.EphemeralStorage, st.TmpSizeLimit = strings.TrimSpace(st.EphemeralStorage), strings.TrimSpace(st.TmpSizeLimit)
	st.CABundle = CABundle{ConfigMap: strings.TrimSpace(st.CABundle.ConfigMap), Secret: strings.TrimSpace(st.CABundle.Secret), Key: strings.TrimSpace(st.CABundle.Key)}
	if len(st.NodeSelector) == 0 {
		st.NodeSelector = nil
	}
	if len(st.Tolerations) == 0 {
		st.Tolerations = nil
	}
	if len(st.Affinity) == 0 {
		st.Affinity = nil
	}
	if len(st.PodLabels) == 0 {
		st.PodLabels = nil
	}
	if len(st.PodAnnotations) == 0 {
		st.PodAnnotations = nil
	}
	return st
}

// Seed copies the config file's settings into the database on the first
// start with one (or after the config file switches executors). Afterwards the database wins; differs names the config
// file values that are ignored.
func (s *Service) Seed(ctx context.Context) (seeded bool, differs []string, err error) {
	if !s.Editable() {
		return false, nil, nil
	}
	file := clean(FromConfig(s.exec))
	stored, found, err := s.Get(ctx)
	if err != nil {
		return false, nil, err
	}
	if !found {
		// Stored as they are: they are what reviews ran with so far.
		return true, nil, s.st.PutSetting(ctx, Key, file, SeedActor)
	}
	stored = clean(stored)
	// 0.6.0 split repo:tag@digest at "@" only, so it stored the bare digest
	// as the version (issue #41). Keep the readable tag with it.
	if _, d, ok := strings.Cut(file.Version, "@"); ok && stored.Version == d {
		stored.Version = file.Version
		if err := s.st.PutSetting(ctx, Key, stored, SeedActor); err != nil {
			return false, nil, err
		}
	}
	a, b := fields(file), fields(stored)
	for _, k := range []string{"version", "cpu", "memory", "ttl", "ephemeral_storage", "tmp_size_limit", "node_selector", "tolerations", "affinity", "priority_class_name", "pod_labels", "pod_annotations", "ca_bundle"} {
		if _, set := a[k]; set && !reflect.DeepEqual(a[k], b[k]) {
			differs = append(differs, k)
		}
	}
	return false, differs, nil
}

// fields is s as a JSON object (only set fields).
func fields(s Settings) map[string]any {
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, v := range m {
		if v == "" || reflect.DeepEqual(v, map[string]any{}) {
			delete(m, k)
		}
	}
	return m
}

// shortDuration is d.String() without zero trailing units: 10m, not 10m0s.
func shortDuration(d time.Duration) string {
	out := d.String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}
