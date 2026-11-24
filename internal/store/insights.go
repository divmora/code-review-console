package store

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// InsightsFilter selects a dashboard's period and repositories (issue
// #155). Repos nil means every repository; an empty, non-nil list none.
type InsightsFilter struct {
	From, To time.Time
	Repos    []RepoRef
	// Lowest is how many lowest-scoring merge requests to return.
	Lowest int
}

// RepoRef names a repository (lower-case host and path, as stored in the
// rollups).
type RepoRef struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Repo     string `json:"repo"`
}

// Insights are a dashboard's numbers over a period, for its repositories
// only: a daily trend (reviews, average scores, findings by severity), each
// repository's latest scores, and the lowest-scoring merge requests.
type Insights struct {
	Daily  []ImpactPoint `json:"daily"`
	Latest []ScoredPR    `json:"latest"`
	Lowest []ScoredPR    `json:"lowest"`
}

// ScoredPR is a merge request's latest scored review.
type ScoredPR struct {
	Platform string    `json:"platform"`
	Host     string    `json:"host"`
	Repo     string    `json:"repo"`
	Number   int       `json:"number"`
	URL      string    `json:"url,omitempty"`
	At       time.Time `json:"at"`
	Scores   Scores    `json:"scores"`
}

// Min is the lowest of the three scores.
func (p ScoredPR) Min() int {
	return min(p.Scores.Quality, p.Scores.Security, p.Scores.Maintainability)
}

// repoArrays turns refs into three parallel arrays for unnest.
func repoArrays(refs []RepoRef) (platforms, hosts, repos []string) {
	for _, r := range refs {
		p, h, path := RepoKey(r.Platform, r.Host, r.Repo)
		platforms, hosts, repos = append(platforms, p), append(hosts, h), append(repos, path)
	}
	return platforms, hosts, repos
}

func (p *Postgres) Insights(ctx context.Context, f InsightsFilter) (*Insights, error) {
	out := &Insights{Daily: []ImpactPoint{}, Latest: []ScoredPR{}, Lowest: []ScoredPR{}}
	if f.Repos != nil && len(f.Repos) == 0 {
		return out, nil
	}
	// The scope: every repository, or the listed ones.
	scope := func(platform, host, repo string, n int) (string, []any) {
		if f.Repos == nil {
			return "", nil
		}
		ps, hs, rs := repoArrays(f.Repos)
		return ` AND (` + platform + `, ` + host + `, ` + repo + `) IN (SELECT * FROM unnest($` + itoa(n) + `::text[], $` + itoa(n+1) + `::text[], $` + itoa(n+2) + `::text[]))`,
			[]any{ps, hs, rs}
	}
	points := map[time.Time]*ImpactPoint{}
	point := func(b time.Time) *ImpactPoint {
		b = b.UTC()
		if points[b] == nil {
			points[b] = &ImpactPoint{Bucket: b, Findings: map[string]int{}}
		}
		return points[b]
	}
	cond, args := scope("platform", "host", "repo", 3)
	rows, err := p.pool.Query(ctx, `SELECT bucket, sum(reviews), sum(comments), sum(scored), sum(quality_sum), sum(security_sum), sum(maintainability_sum)
		FROM job_metrics WHERE granularity = 'day' AND bucket >= $1 AND bucket < $2`+cond+` GROUP BY 1`, append([]any{f.From, f.To}, args...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b time.Time
		var ip ImpactPoint
		if err := rows.Scan(&b, &ip.Reviews, &ip.Comments, &ip.Scored, &ip.QualitySum, &ip.SecuritySum, &ip.MaintainabilitySum); err != nil {
			rows.Close()
			return nil, err
		}
		pt := point(b)
		pt.Comments = ip.Comments
		pt.Reviews, pt.Scored, pt.QualitySum, pt.SecuritySum, pt.MaintainabilitySum = ip.Reviews, ip.Scored, ip.QualitySum, ip.SecuritySum, ip.MaintainabilitySum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = p.pool.Query(ctx, `SELECT bucket, severity, sum(findings) FROM finding_metrics
		WHERE granularity = 'day' AND bucket >= $1 AND bucket < $2`+cond+` GROUP BY 1, 2`, append([]any{f.From, f.To}, args...)...)
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
	for _, pt := range points {
		out.Daily = append(out.Daily, *pt)
	}
	sortPoints(out.Daily)

	scan := func(q string, args ...any) ([]ScoredPR, error) {
		rows, err := p.pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		list := []ScoredPR{}
		for rows.Next() {
			var s ScoredPR
			if err := rows.Scan(&s.Platform, &s.Host, &s.Repo, &s.Number, &s.URL, &s.At, &s.Scores.Quality, &s.Scores.Security, &s.Scores.Maintainability); err != nil {
				return nil, err
			}
			list = append(list, s)
		}
		return list, rows.Err()
	}
	jcond, jargs := scope("platform", "lower(host)", "lower(repo)", 1)
	if out.Latest, err = scan(`SELECT DISTINCT ON (platform, lower(host), lower(repo)) platform, lower(host), lower(repo), number, url, finished,
		score_quality, score_security, score_maintainability FROM jobs WHERE score_quality IS NOT NULL`+jcond+`
		ORDER BY platform, lower(host), lower(repo), id DESC`, jargs...); err != nil {
		return nil, err
	}
	if f.Lowest > 0 {
		lcond, largs := scope("platform", "lower(host)", "lower(repo)", 3)
		latest, err := scan(`SELECT DISTINCT ON (platform, lower(host), lower(repo), number) platform, lower(host), lower(repo), number, url, finished,
			score_quality, score_security, score_maintainability FROM jobs
			WHERE score_quality IS NOT NULL AND finished >= $1 AND finished < $2`+lcond+`
			ORDER BY platform, lower(host), lower(repo), number, id DESC`, append([]any{f.From, f.To}, largs...)...)
		if err != nil {
			return nil, err
		}
		out.Lowest = lowestOf(latest, f.Lowest)
	}
	return out, nil
}

// lowestOf returns the n merge requests with the lowest minimum score
// (then the oldest review first, for a stable order).
func lowestOf(prs []ScoredPR, n int) []ScoredPR {
	sortScored(prs)
	if len(prs) > n {
		prs = prs[:n]
	}
	return prs
}

func itoa(n int) string { return strconv.Itoa(n) }

func sortPoints(ps []ImpactPoint) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Bucket.Before(ps[j].Bucket) })
}

func sortScored(prs []ScoredPR) {
	sort.SliceStable(prs, func(i, j int) bool {
		if a, b := prs[i].Min(), prs[j].Min(); a != b {
			return a < b
		}
		return prs[i].At.Before(prs[j].At)
	})
}

// SortScored orders merge requests lowest minimum score first (for other
// stores).
func SortScored(prs []ScoredPR) { sortScored(prs) }
