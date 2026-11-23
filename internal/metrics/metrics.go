// Package metrics writes the console's metrics in the Prometheus text
// format (version 0.0.4), which Prometheus, VictoriaMetrics, the
// OpenTelemetry collector, Datadog and Grafana Agent all scrape. It keeps
// counters and histograms in memory, per replica: Prometheus adds the
// replicas up. It has no dependencies on purpose.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Labels are a metric's label values, by name.
type Labels map[string]string

func (l Labels) key() string {
	if len(l) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for n := range l {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escape(l[n]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escape(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(v)
}

func with(key, name, value string) string {
	extra := name + `="` + escape(value) + `"`
	if key == "" {
		return "{" + extra + "}"
	}
	return key[:len(key)-1] + "," + extra + "}"
}

func num(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// Counter is a counter with labels.
type Counter struct {
	name, help string
	mu         sync.Mutex
	values     map[string]float64
}

// Add adds v (>= 0) to the series with labels l.
func (c *Counter) Add(l Labels, v float64) {
	if v < 0 {
		return
	}
	c.mu.Lock()
	c.values[l.key()] += v
	c.mu.Unlock()
}

// Inc adds 1.
func (c *Counter) Inc(l Labels) { c.Add(l, 1) }

// Value is the series' current value (for tests).
func (c *Counter) Value(l Labels) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[l.key()]
}

func (c *Counter) write(w io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	header(w, c.name, c.help, "counter")
	for _, k := range sortedKeys(c.values) {
		_, _ = fmt.Fprintf(w, "%s%s %s\n", c.name, k, num(c.values[k]))
	}
}

// Histogram counts observations into buckets, with labels.
type Histogram struct {
	name, help string
	buckets    []float64
	mu         sync.Mutex
	series     map[string]*hseries
}

type hseries struct {
	counts []uint64
	sum    float64
	count  uint64
}

// Observe records v for the series with labels l.
func (h *Histogram) Observe(l Labels, v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := l.key()
	s := h.series[k]
	if s == nil {
		s = &hseries{counts: make([]uint64, len(h.buckets))}
		h.series[k] = s
	}
	for i, b := range h.buckets {
		if v <= b {
			s.counts[i]++
		}
	}
	s.sum += v
	s.count++
}

func (h *Histogram) write(w io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	header(w, h.name, h.help, "histogram")
	keys := make([]string, 0, len(h.series))
	for k := range h.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := h.series[k]
		for i, b := range h.buckets {
			_, _ = fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, with(k, "le", num(b)), s.counts[i])
		}
		_, _ = fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, with(k, "le", "+Inf"), s.count)
		_, _ = fmt.Fprintf(w, "%s_sum%s %s\n", h.name, k, num(s.sum))
		_, _ = fmt.Fprintf(w, "%s_count%s %d\n", h.name, k, s.count)
	}
}

// Sample is one value of a gauge read at scrape time.
type Sample struct {
	Labels Labels
	Value  float64
}

type gauge struct {
	name, help string
	read       func() []Sample
}

func (g gauge) write(w io.Writer) {
	samples := g.read()
	header(w, g.name, g.help, "gauge")
	sort.Slice(samples, func(i, j int) bool { return samples[i].Labels.key() < samples[j].Labels.key() })
	for _, s := range samples {
		_, _ = fmt.Fprintf(w, "%s%s %s\n", g.name, s.Labels.key(), num(s.Value))
	}
}

func header(w io.Writer, name, help, kind string) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, strings.ReplaceAll(help, "\n", " "), name, kind)
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type writer interface{ write(io.Writer) }

// Registry holds metrics in the order they were added.
type Registry struct {
	mu   sync.Mutex
	list []writer
}

// Counter adds a counter.
func (r *Registry) Counter(name, help string) *Counter {
	c := &Counter{name: name, help: help, values: map[string]float64{}}
	r.add(c)
	return c
}

// Histogram adds a histogram with the given upper bounds (ascending).
func (r *Registry) Histogram(name, help string, buckets []float64) *Histogram {
	h := &Histogram{name: name, help: help, buckets: buckets, series: map[string]*hseries{}}
	r.add(h)
	return h
}

// Gauge adds a gauge read when scraped.
func (r *Registry) Gauge(name, help string, read func() []Sample) {
	r.add(gauge{name: name, help: help, read: read})
}

func (r *Registry) add(m writer) {
	r.mu.Lock()
	r.list = append(r.list, m)
	r.mu.Unlock()
}

// ContentType is the Prometheus text format's content type.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Write writes every metric.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	list := append([]writer{}, r.list...)
	r.mu.Unlock()
	for _, m := range list {
		m.write(w)
	}
}
