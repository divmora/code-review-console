package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Impact page (issue #102) shows what the console has done for an
// installation. Everything it reads is kept as rollups, like job_metrics,
// so it covers the whole history even after old jobs are pruned:
//
//   - finding_metrics: findings per day and month, by severity and category
//   - reviewed_prs: each reviewed merge request, with its first review
//   - pr_review_days: which merge requests were reviewed on which day
//   - job_metrics.reviews: review runs (done is every finished job)

// Severities are the finding severities, most severe first.
var Severities = []string{"critical", "major", "minor", "nitpick"}

// IsReview reports whether a finished job counts as a review run.
func IsReview(j Job) bool {
	return (j.Kind == "review" || j.Kind == "full review") && j.State == "done"
}

// FindingCount is a number of findings of one severity and category.
type FindingCount struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Count    int    `json:"count"`
}

// CountFindings counts a review result's findings by severity and
// category. Questions are not findings. Unknown severities count as
// "other", and a missing category as "other".
func CountFindings(result json.RawMessage) []FindingCount {
	if len(result) == 0 {
		return nil
	}
	var r struct {
		Findings []struct {
			Severity string `json:"severity"`
			Category string `json:"category"`
			Kind     string `json:"kind"`
		} `json:"findings"`
	}
	if json.Unmarshal(result, &r) != nil {
		return nil
	}
	by := map[[2]string]int{}
	var order [][2]string
	for _, f := range r.Findings {
		if f.Kind == "question" {
			continue
		}
		k := [2]string{normSeverity(f.Severity), normCategory(f.Category)}
		if by[k] == 0 {
			order = append(order, k)
		}
		by[k]++
	}
	out := make([]FindingCount, 0, len(order))
	for _, k := range order {
		out = append(out, FindingCount{Severity: k[0], Category: k[1], Count: by[k]})
	}
	return out
}

func normSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, v := range Severities {
		if s == v {
			return s
		}
	}
	return "other"
}

func normCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if c == "" || len(c) > 40 {
		return "other"
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != ' ' {
			return "other"
		}
	}
	return c
}

// ImpactFilter selects the Impact summary's period. Granularity is the
// trend's bucket: Daily or Monthly.
type ImpactFilter struct {
	From, To    time.Time
	Granularity string
	// Top is how many repositories the top lists hold (0: none).
	Top int
}

// Impact is what the console did in a period.
type Impact struct {
	// PRsReviewed counts distinct merge requests with a review run in the
	// period; ReviewRuns every review run (a push can review again).
	PRsReviewed int `json:"prs_reviewed"`
	ReviewRuns  int `json:"review_runs"`
	// ActiveRepos have had a review run in the period.
	ActiveRepos int `json:"active_repos"`
	Findings    int `json:"findings"`
	// BySeverity is critical, major, minor, nitpick and other.
	BySeverity map[string]int `json:"by_severity"`
	ByCategory []FindingCount `json:"by_category"`
	// SecurityFindings are findings in the security category.
	SecurityFindings int `json:"security_findings"`
	Comments         int `json:"comments"`
	// Approvals counts approvals the bot gave on the code host (a review
	// that approved; one already approved isn't counted again).
	Approvals int `json:"approvals"`
	// FirstReviews are merge requests first reviewed in the period, and
	// MedianFirstReviewMS the median time from the webhook to their first
	// review's comments (including the wait after a push).
	FirstReviews        int           `json:"first_reviews"`
	MedianFirstReviewMS int64         `json:"median_first_review_ms"`
	Scored              int           `json:"scored"`
	QualitySum          int64         `json:"quality_sum"`
	SecuritySum         int64         `json:"security_sum"`
	MaintainabilitySum  int64         `json:"maintainability_sum"`
	Trend               []ImpactPoint `json:"trend"`
	TopByReviews        []RepoCount   `json:"top_by_reviews"`
	TopByFindings       []RepoCount   `json:"top_by_findings"`
}

// ImpactPoint is one trend bucket.
type ImpactPoint struct {
	Bucket             time.Time      `json:"bucket"`
	Reviews            int            `json:"reviews"`
	Findings           map[string]int `json:"findings"` // by severity
	Scored             int            `json:"scored"`
	QualitySum         int64          `json:"quality_sum"`
	SecuritySum        int64          `json:"security_sum"`
	MaintainabilitySum int64          `json:"maintainability_sum"`
}

// RepoCount is a repository with a count (reviews or findings).
type RepoCount struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Repo     string `json:"repo"`
	Count    int    `json:"count"`
}

// impactSQL creates the Impact rollups and fills them from the jobs
// already stored (migration 19).
const impactSQL = `
	ALTER TABLE job_metrics ADD COLUMN reviews integer NOT NULL DEFAULT 0;
	CREATE TABLE finding_metrics (
		granularity text NOT NULL,
		bucket      timestamptz NOT NULL,
		platform    text NOT NULL,
		host        text NOT NULL,
		repo        text NOT NULL,
		severity    text NOT NULL,
		category    text NOT NULL,
		findings    integer NOT NULL DEFAULT 0,
		PRIMARY KEY (granularity, bucket, platform, host, repo, severity, category)
	);
	CREATE INDEX finding_metrics_bucket_idx ON finding_metrics (granularity, bucket);
	CREATE TABLE reviewed_prs (
		platform         text NOT NULL,
		host             text NOT NULL,
		repo             text NOT NULL,
		number           integer NOT NULL,
		first_at         timestamptz NOT NULL,
		first_latency_ms bigint NOT NULL,
		PRIMARY KEY (platform, host, repo, number)
	);
	CREATE INDEX reviewed_prs_first_idx ON reviewed_prs (first_at);
	CREATE TABLE pr_review_days (
		day      date NOT NULL,
		platform text NOT NULL,
		host     text NOT NULL,
		repo     text NOT NULL,
		number   integer NOT NULL,
		PRIMARY KEY (day, platform, host, repo, number)
	);` + impactBackfill

// impactBackfill fills the Impact rollups from stored jobs. Rows already
// there are kept, so it only adds what is missing.
const impactBackfill = `
	UPDATE job_metrics m SET reviews = s.n
	FROM (SELECT g, date_trunc(g, finished AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket, platform, host, lower(repo) AS repo, count(*) AS n
		FROM jobs, unnest(ARRAY['hour', 'day', 'month']) AS g
		WHERE kind IN ('review', 'full review') AND state = 'done' GROUP BY 1, 2, 3, 4, 5) s
	WHERE m.granularity = s.g AND m.bucket = s.bucket AND m.platform = s.platform AND m.host = s.host AND m.repo = s.repo AND m.reviews = 0;
	INSERT INTO finding_metrics (granularity, bucket, platform, host, repo, severity, category, findings)
	SELECT g, date_trunc(g, j.finished AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', j.platform, j.host, lower(j.repo),
		CASE WHEN lower(f->>'severity') IN ('critical', 'major', 'minor', 'nitpick') THEN lower(f->>'severity') ELSE 'other' END,
		CASE WHEN coalesce(trim(f->>'category'), '') ~ '^[A-Za-z0-9_ -]{1,40}$' THEN lower(trim(f->>'category')) ELSE 'other' END,
		count(*)
	FROM jobs j, unnest(ARRAY['day', 'month']) AS g, jsonb_array_elements(CASE WHEN jsonb_typeof(j.result->'findings') = 'array' THEN j.result->'findings' ELSE '[]'::jsonb END) AS f
	WHERE j.kind IN ('review', 'full review') AND j.state = 'done' AND coalesce(f->>'kind', '') <> 'question'
	GROUP BY 1, 2, 3, 4, 5, 6, 7
	ON CONFLICT DO NOTHING;
	INSERT INTO reviewed_prs (platform, host, repo, number, first_at, first_latency_ms)
	SELECT DISTINCT ON (platform, host, lower(repo), number) platform, host, lower(repo), number, finished,
		greatest(0, extract(epoch FROM (finished - enqueued)) * 1000)::bigint
	FROM jobs WHERE kind IN ('review', 'full review') AND state = 'done'
	ORDER BY platform, host, lower(repo), number, finished
	ON CONFLICT DO NOTHING;
	INSERT INTO pr_review_days (day, platform, host, repo, number)
	SELECT DISTINCT (finished AT TIME ZONE 'UTC')::date, platform, host, lower(repo), number
	FROM jobs WHERE kind IN ('review', 'full review') AND state = 'done'
	ON CONFLICT DO NOTHING;`

// recordImpact adds a finished review run to the Impact rollups, in the
// job's insert transaction.
func recordImpact(ctx context.Context, tx pgx.Tx, j Job) error {
	if !IsReview(j) {
		return nil
	}
	for _, f := range CountFindings(j.Result) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO finding_metrics (granularity, bucket, platform, host, repo, severity, category, findings)
			SELECT g, date_trunc(g, $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', $2, $3, lower($4), $5, $6, $7
			FROM unnest(ARRAY['day', 'month']) AS g
			ON CONFLICT (granularity, bucket, platform, host, repo, severity, category) DO UPDATE SET
				findings = finding_metrics.findings + EXCLUDED.findings`,
			j.Finished, j.Platform, j.Host, j.Repo, f.Severity, f.Category, f.Count); err != nil {
			return err
		}
	}
	latency := max(0, j.Finished.Sub(j.Enqueued).Milliseconds())
	if _, err := tx.Exec(ctx, `
		INSERT INTO reviewed_prs (platform, host, repo, number, first_at, first_latency_ms) VALUES ($1, $2, lower($3), $4, $5, $6)
		ON CONFLICT DO NOTHING`, j.Platform, j.Host, j.Repo, j.Number, j.Finished, latency); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO pr_review_days (day, platform, host, repo, number) VALUES (($1::timestamptz AT TIME ZONE 'UTC')::date, $2, $3, lower($4), $5)
		ON CONFLICT DO NOTHING`, j.Finished, j.Platform, j.Host, j.Repo, j.Number)
	return err
}

func (p *Postgres) Impact(ctx context.Context, f ImpactFilter) (*Impact, error) {
	if f.Granularity != Monthly {
		f.Granularity = Daily
	}
	out := &Impact{BySeverity: map[string]int{}, ByCategory: []FindingCount{}, Trend: []ImpactPoint{}, TopByReviews: []RepoCount{}, TopByFindings: []RepoCount{}}
	// Rollup buckets are UTC days or months: a bucket counts when it
	// starts inside [From, To).
	if err := p.pool.QueryRow(ctx, `
		SELECT coalesce(sum(reviews), 0), coalesce(sum(comments), 0), coalesce(sum(scored), 0), coalesce(sum(quality_sum), 0),
			coalesce(sum(security_sum), 0), coalesce(sum(maintainability_sum), 0), coalesce(sum(approvals), 0)
		FROM job_metrics WHERE granularity = $1 AND bucket >= $2 AND bucket < $3`, f.Granularity, f.From, f.To).
		Scan(&out.ReviewRuns, &out.Comments, &out.Scored, &out.QualitySum, &out.SecuritySum, &out.MaintainabilitySum, &out.Approvals); err != nil {
		return nil, err
	}
	if err := p.pool.QueryRow(ctx, `
		SELECT count(DISTINCT (platform, host, repo, number)), count(DISTINCT (platform, host, repo))
		FROM pr_review_days WHERE day >= ($1::timestamptz AT TIME ZONE 'UTC')::date AND day < ($2::timestamptz AT TIME ZONE 'UTC')::date`, f.From, f.To).
		Scan(&out.PRsReviewed, &out.ActiveRepos); err != nil {
		return nil, err
	}
	var median *float64
	if err := p.pool.QueryRow(ctx, `
		SELECT count(*), percentile_cont(0.5) WITHIN GROUP (ORDER BY first_latency_ms)
		FROM reviewed_prs WHERE first_at >= $1 AND first_at < $2`, f.From, f.To).Scan(&out.FirstReviews, &median); err != nil {
		return nil, err
	}
	if median != nil {
		out.MedianFirstReviewMS = int64(*median)
	}
	rows, err := p.pool.Query(ctx, `
		SELECT severity, category, sum(findings) FROM finding_metrics
		WHERE granularity = $1 AND bucket >= $2 AND bucket < $3 GROUP BY 1, 2 ORDER BY 3 DESC, 1, 2`, f.Granularity, f.From, f.To)
	if err != nil {
		return nil, err
	}
	cats := map[string]int{}
	var catOrder []string
	for rows.Next() {
		var fc FindingCount
		if err := rows.Scan(&fc.Severity, &fc.Category, &fc.Count); err != nil {
			rows.Close()
			return nil, err
		}
		out.Findings += fc.Count
		out.BySeverity[fc.Severity] += fc.Count
		if fc.Category == "security" {
			out.SecurityFindings += fc.Count
		}
		if _, ok := cats[fc.Category]; !ok {
			catOrder = append(catOrder, fc.Category)
		}
		cats[fc.Category] += fc.Count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range catOrder {
		out.ByCategory = append(out.ByCategory, FindingCount{Category: c, Count: cats[c]})
	}
	sortCounts(out.ByCategory)

	// The trend: review runs and scores from job_metrics, findings from
	// finding_metrics, by bucket.
	points := map[time.Time]*ImpactPoint{}
	point := func(b time.Time) *ImpactPoint {
		b = b.UTC()
		if points[b] == nil {
			points[b] = &ImpactPoint{Bucket: b, Findings: map[string]int{}}
		}
		return points[b]
	}
	rows, err = p.pool.Query(ctx, `
		SELECT bucket, sum(reviews), sum(scored), sum(quality_sum), sum(security_sum), sum(maintainability_sum)
		FROM job_metrics WHERE granularity = $1 AND bucket >= $2 AND bucket < $3 GROUP BY 1`, f.Granularity, f.From, f.To)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b time.Time
		var ip ImpactPoint
		if err := rows.Scan(&b, &ip.Reviews, &ip.Scored, &ip.QualitySum, &ip.SecuritySum, &ip.MaintainabilitySum); err != nil {
			rows.Close()
			return nil, err
		}
		pt := point(b)
		pt.Reviews, pt.Scored, pt.QualitySum, pt.SecuritySum, pt.MaintainabilitySum = ip.Reviews, ip.Scored, ip.QualitySum, ip.SecuritySum, ip.MaintainabilitySum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = p.pool.Query(ctx, `
		SELECT bucket, severity, sum(findings) FROM finding_metrics
		WHERE granularity = $1 AND bucket >= $2 AND bucket < $3 GROUP BY 1, 2`, f.Granularity, f.From, f.To)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b time.Time
		var sev string
		var n int
		if err := rows.Scan(&b, &sev, &n); err != nil {
			rows.Close()
			return nil, err
		}
		point(b).Findings[sev] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Trend = SortedPoints(points)

	if f.Top > 0 {
		if out.TopByReviews, err = p.topRepos(ctx, `SELECT platform, host, repo, sum(reviews) FROM job_metrics
			WHERE granularity = $1 AND bucket >= $2 AND bucket < $3 GROUP BY 1, 2, 3 HAVING sum(reviews) > 0 ORDER BY 4 DESC, 3 LIMIT $4`, f); err != nil {
			return nil, err
		}
		if out.TopByFindings, err = p.topRepos(ctx, `SELECT platform, host, repo, sum(findings) FROM finding_metrics
			WHERE granularity = $1 AND bucket >= $2 AND bucket < $3 GROUP BY 1, 2, 3 ORDER BY 4 DESC, 3 LIMIT $4`, f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *Postgres) topRepos(ctx context.Context, q string, f ImpactFilter) ([]RepoCount, error) {
	rows, err := p.pool.Query(ctx, q, f.Granularity, f.From, f.To, f.Top)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoCount{}
	for rows.Next() {
		var r RepoCount
		if err := rows.Scan(&r.Platform, &r.Host, &r.Repo, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sortCounts orders by count, most first, then by category.
func sortCounts(cs []FindingCount) {
	sort.SliceStable(cs, func(i, k int) bool {
		if cs[i].Count != cs[k].Count {
			return cs[i].Count > cs[k].Count
		}
		return cs[i].Category < cs[k].Category
	})
}

// SortedPoints returns the trend points oldest first.
func SortedPoints(m map[time.Time]*ImpactPoint) []ImpactPoint {
	out := make([]ImpactPoint, 0, len(m))
	for _, p := range m {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Bucket.Before(out[k].Bucket) })
	return out
}

// SortCounts is sortCounts for the test fake.
func SortCounts(cs []FindingCount) { sortCounts(cs) }
