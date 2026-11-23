package admin

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/store"
)

// The Impact page (issue #102): what the console has done for this
// installation, from its own database only. The headline numbers for the
// last 30 days are free; longer ranges, the trends, the breakdowns and the
// export are part of the metrics feature.

// impactRanges are the periods the page offers, in days (0: all history).
var impactRanges = map[string]int{"30d": 30, "90d": 90, "365d": 365, "all": 0}

// impactFreeRange is the range the free core shows.
const impactFreeRange = "30d"

// impactTop is how many repositories the top lists hold.
const impactTop = 10

type impactView struct {
	Range       string    `json:"range"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Granularity string    `json:"granularity"`
	// Licensed is false in the free core: only the headline numbers are
	// filled in.
	Licensed bool `json:"licensed"`
	store.Impact
}

// impactPeriod turns a range into UTC bucket bounds: whole days for 30
// and 90 days, whole months for a year and all history.
func impactPeriod(rng string, now time.Time) (from, to time.Time, gran string) {
	now = now.UTC()
	days := impactRanges[rng]
	switch {
	case days == 0: // all history
		return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0), store.Monthly
	case days > 90:
		to = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		return to.AddDate(0, -12, 0), to, store.Monthly
	}
	to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return to.AddDate(0, 0, -days), to, store.Daily
}

func (a *Admin) impact(w http.ResponseWriter, r *http.Request) (impactView, bool) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = impactFreeRange
	}
	if _, ok := impactRanges[rng]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "range: want 30d, 90d, 365d or all"})
		return impactView{}, false
	}
	licensed := a.Lic.Has(license.Metrics)
	if rng != impactFreeRange && !a.require(w, license.Metrics) {
		return impactView{}, false
	}
	from, to, gran := impactPeriod(rng, time.Now())
	f := store.ImpactFilter{From: from, To: to, Granularity: gran}
	if licensed {
		f.Top = impactTop
	}
	im, err := a.store.Impact(r.Context(), f)
	if err != nil {
		a.serverError(w, err)
		return impactView{}, false
	}
	if !licensed { // the headline numbers only
		im.Trend, im.ByCategory, im.TopByReviews, im.TopByFindings = []store.ImpactPoint{}, []store.FindingCount{}, []store.RepoCount{}, []store.RepoCount{}
	}
	return impactView{Range: rng, From: from, To: to, Granularity: gran, Licensed: licensed, Impact: *im}, true
}

// apiImpact is GET /admin/api/impact?range=30d|90d|365d|all.
func (a *Admin) apiImpact(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if v, ok := a.impact(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// apiImpactExport downloads the summary as CSV or JSON, for reports.
func (a *Admin) apiImpactExport(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Metrics) {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format: want csv or json"})
		return
	}
	v, ok := a.impact(w, r)
	if !ok {
		return
	}
	_ = a.store.Audit(r.Context(), s.Email, "impact.export", v.Range, map[string]any{"format": format})
	name := "impact-" + v.Range + "-" + time.Now().UTC().Format("20060102") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	row := func(cells ...string) { _ = cw.Write(cells) }
	n := func(v int) string { return strconv.Itoa(v) }
	avg := func(sum int64) string {
		if v.Scored == 0 {
			return ""
		}
		return strconv.FormatFloat(float64(sum)/float64(v.Scored), 'f', 1, 64)
	}
	row("section", "name", "value")
	for _, x := range [][2]string{
		{"from", v.From.Format(time.RFC3339)}, {"to", v.To.Format(time.RFC3339)},
		{"merge_requests_reviewed", n(v.PRsReviewed)}, {"review_runs", n(v.ReviewRuns)}, {"active_repositories", n(v.ActiveRepos)},
		{"findings", n(v.Findings)}, {"security_findings", n(v.SecurityFindings)}, {"comments_posted", n(v.Comments)},
		{"first_reviews", n(v.FirstReviews)}, {"median_time_to_first_review_seconds", strconv.FormatFloat(float64(v.MedianFirstReviewMS)/1000, 'f', 1, 64)},
		{"scored_reviews", n(v.Scored)}, {"average_quality", avg(v.QualitySum)}, {"average_security", avg(v.SecuritySum)},
		{"average_maintainability", avg(v.MaintainabilitySum)},
	} {
		row("summary", x[0], x[1])
	}
	for _, sev := range append(append([]string{}, store.Severities...), "other") {
		row("severity", sev, n(v.BySeverity[sev]))
	}
	for _, c := range v.ByCategory {
		row("category", csvSafe(c.Category), n(c.Count))
	}
	for _, rc := range v.TopByReviews {
		row("top_by_reviews", csvSafe(rc.Platform+":"+rc.Host+"/"+rc.Repo), n(rc.Count))
	}
	for _, rc := range v.TopByFindings {
		row("top_by_findings", csvSafe(rc.Platform+":"+rc.Host+"/"+rc.Repo), n(rc.Count))
	}
	for _, p := range v.Trend {
		total := 0
		for _, c := range p.Findings {
			total += c
		}
		row("trend_reviews", p.Bucket.Format(time.RFC3339), n(p.Reviews))
		row("trend_findings", p.Bucket.Format(time.RFC3339), n(total))
	}
	cw.Flush()
}
