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

// pollCacheEntry memoizes the last queued-jobs count per runs-endpoint ETag
// (docs/24 §5.2): a 304 response skips the per-run jobs listings entirely and
// replays the cached count. Conditional requests are exempt from the primary
// rate limit, making the steady-state no-change poll one free request.
type pollCacheEntry struct {
	etag   string
	queued int
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

// PollQueuedJobs implements demand polling for GitHub (docs/24 §5.2): a
// two-step query listing the repository's queued workflow runs, then each
// run's jobs, counting jobs that are queued and whose labels the pool
// satisfies. Org- and global-scoped targets return ErrPollingScopeUnsupported:
// the GitHub REST API exposes no org-level queued-runs listing (docs/24 §4),
// so those targets keep webhook-only demand.
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

	// per_page=100: a repo with more concurrently queued runs than this is far
	// beyond the fallback path's scale, and the deficit is already capped by
	// max_concurrency, so under-counting beyond one page cannot overspawn.
	runsURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs?status=queued&per_page=100", c.baseURL, owner, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, runsURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	c.setCommonHeaders(req)

	cacheKey := runsURL
	pollETagCache.Lock()
	entry, cached := pollETagCache.entries[cacheKey]
	pollETagCache.Unlock()
	if cached {
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("polling queued runs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Queued-runs set unchanged since the last poll (docs/24 §5.4).
		return entry.queued, nil
	case http.StatusOK:
		// fall through to the listing below
	default:
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("queued runs listing failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	etag := resp.Header.Get("ETag")

	var listResp struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return 0, fmt.Errorf("decoding queued runs response: %w", err)
	}

	queued := 0
	for _, run := range listResp.WorkflowRuns {
		count, err := c.countQueuedJobsForRun(ctx, authToken, owner, repo, run.ID, target.Labels)
		if err != nil {
			return 0, err
		}
		queued += count
	}

	pollETagCache.Lock()
	if len(pollETagCache.entries) >= maxPollCacheEntries {
		pollETagCache.entries = make(map[string]pollCacheEntry)
	}
	pollETagCache.entries[cacheKey] = pollCacheEntry{etag: etag, queued: queued}
	pollETagCache.Unlock()

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
