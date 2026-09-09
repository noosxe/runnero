package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/noosxe/runnero/internal/provider"
)

// pollCacheEntry memoizes the last queued-jobs count per queued-runs-endpoint
// ETag (docs/24 §5.2): a 304 response skips the per-run jobs listings entirely
// and replays the cached count. Conditional requests are exempt from the primary
// rate limit, making the steady-state no-change poll one free request.
//
// Only the queued-runs listing is cached: a run leaves the queued set the
// moment any of its jobs starts, so an unchanged queued set implies the cached
// count is still exact. The in-progress listing is deliberately recounted on
// every poll (RUN-146) — jobs inside an in-progress run transition without
// changing the run-set membership, so a cached count would go stale until the
// next run-set change.
type pollCacheEntry struct {
	etag  string
	count int
}

// pollETagCache is package-level because provider clients are rebuilt on every
// resolve (Registry.BuildFromDB) — a client-scoped cache would never warm.
var pollETagCache = struct {
	sync.Mutex
	entries map[string]pollCacheEntry
}{entries: make(map[string]pollCacheEntry)}

// maxPollCacheEntries bounds the memoization map; eviction is a wholesale
// reset — losing warm entries only costs one cold poll per target.
const maxPollCacheEntries = 1024

// PollQueuedJobs implements demand polling for GitHub (docs/24 §5.2): per
// runs listing it lists the repository's workflow runs, then each run's jobs,
// counting jobs that are queued and whose labels the pool satisfies. Two
// listings are polled (RUN-146): status=queued and status=in_progress.
// GitHub flips a run queued → in_progress as soon as any of its jobs starts,
// so a multi-job run with one job executing and another still queued only
// ever appears in the in-progress listing — polling both keeps the surviving
// queued jobs visible to the demand signal. Org- and global-scoped targets
// return ErrPollingScopeUnsupported: the GitHub REST API exposes no org-level
// runs listing by status (docs/24 §4), so those targets keep webhook-only
// demand.
func (c *Client) PollQueuedJobs(ctx context.Context, target provider.PollTarget) (int, error) {
	if target.Scope != "" && target.Scope != provider.ScopeRepo {
		return 0, provider.ErrPollingScopeUnsupported
	}
	owner, repo, err := parseTargetURL(target.URL)
	if err != nil {
		return 0, err
	}
	if repo == "" {
		return 0, provider.ErrPollingScopeUnsupported
	}

	authToken, err := c.getAuthBearerToken(ctx, owner, repo)
	if err != nil {
		return 0, err
	}

	// The queued listing short-circuits on an unchanged run set via ETag
	// (docs/24 §5.4); the in-progress listing is recounted on every poll —
	// see pollCacheEntry for why the two listings are treated differently.
	queued, err := c.countQueuedJobsInRuns(ctx, authToken, owner, repo, "queued", target.Labels)
	if err != nil {
		return 0, err
	}
	inProgress, err := c.countQueuedJobsInRuns(ctx, authToken, owner, repo, "in_progress", target.Labels)
	if err != nil {
		return 0, err
	}
	return queued + inProgress, nil
}

// countQueuedJobsInRuns lists the repository's workflow runs in the given
// status and counts, per run, the jobs still queued whose labels the pool can
// satisfy (docs/24 §5.2): a job demanding labels the pool's runners do not
// configure would never be picked up by a spawned runner, so counting it
// would overprovision. A run appears in exactly one status listing at a
// time, so the queued and in-progress counts never double-count a run.
//
// Only the queued listing participates in the ETag/304 short-circuit, and the
// cache key includes the pool's label contract: two pools polling the same
// repo with different labels must not replay each other's counts.
func (c *Client) countQueuedJobsInRuns(ctx context.Context, authToken, owner, repo, runStatus, poolLabels string) (int, error) {
	// per_page=100: a repo with more concurrently queued/in-progress runs than
	// this is far beyond the fallback path's scale, and the deficit is already
	// capped by max_concurrency, so under-counting beyond one page cannot
	// overspawn.
	runsURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs?status=%s&per_page=100", c.baseURL, owner, repo, runStatus)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, runsURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	c.setCommonHeaders(req)

	cached := false
	var entry pollCacheEntry
	if runStatus == "queued" {
		cacheKey := runsURL + "\x00" + poolLabels
		pollETagCache.Lock()
		entry, cached = pollETagCache.entries[cacheKey]
		pollETagCache.Unlock()
		if cached {
			req.Header.Set("If-None-Match", entry.etag)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("polling %s runs: %w", runStatus, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Run set unchanged since the last poll (docs/24 §5.4): replay the
		// cached count without touching the per-run jobs listings.
		if !cached {
			return 0, fmt.Errorf("%s runs listing returned 304 without a prior poll", runStatus)
		}
		return entry.count, nil
	case http.StatusOK:
		// fall through to the listing below
	default:
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("%s runs listing failed (status %d): %s", runStatus, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	etag := resp.Header.Get("ETag")

	var listResp struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return 0, fmt.Errorf("decoding %s runs response: %w", runStatus, err)
	}

	queued := 0
	for _, run := range listResp.WorkflowRuns {
		count, err := c.countQueuedJobsForRun(ctx, authToken, owner, repo, run.ID, poolLabels)
		if err != nil {
			return 0, err
		}
		queued += count
	}

	if runStatus == "queued" {
		pollETagCache.Lock()
		if len(pollETagCache.entries) >= maxPollCacheEntries {
			pollETagCache.entries = make(map[string]pollCacheEntry)
		}
		pollETagCache.entries[runsURL+"\x00"+poolLabels] = pollCacheEntry{etag: etag, count: queued}
		pollETagCache.Unlock()
	}

	return queued, nil
}

// countQueuedJobsForRun lists the run's jobs and counts those still queued
// whose labels the pool can satisfy (docs/24 §5.2): a job demanding labels the
// pool's runners do not configure would never be picked up by a spawned
// runner, so counting it would overprovision.
func (c *Client) countQueuedJobsForRun(ctx context.Context, authToken, owner, repo string, runID int64, poolLabels string) (int, error) {
	jobsURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/jobs?per_page=100", c.baseURL, owner, repo, runID)

	// maxJobPages bounds pagination as a loop guard against API misbehaviour;
	// 10 pages × 100 jobs is far beyond any realistic single workflow run.
	const maxJobPages = 10

	total := 0
	for page := 1; page <= maxJobPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s&page=%d", jobsURL, page), nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+authToken)
		c.setCommonHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return 0, fmt.Errorf("listing run jobs: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return 0, fmt.Errorf("run jobs listing failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var listResp struct {
			TotalCount int `json:"total_count"`
			Jobs       []struct {
				ID     int64    `json:"id"`
				Status string   `json:"status"`
				Labels []string `json:"labels"`
			} `json:"jobs"`
		}
		err = json.NewDecoder(resp.Body).Decode(&listResp)
		closeErr := resp.Body.Close()
		if err != nil {
			return 0, fmt.Errorf("decoding run jobs response: %w", err)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("closing run jobs response: %w", closeErr)
		}

		for _, j := range listResp.Jobs {
			if j.Status == "queued" && provider.LabelsMatch(poolLabels, j.Labels) {
				total++
			}
		}

		// Stop at a short page; the API never pads trailing pages.
		if len(listResp.Jobs) < 100 {
			break
		}
	}
	return total, nil
}
