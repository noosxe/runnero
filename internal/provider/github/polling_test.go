package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/provider/github"
)

// pollServer simulates the GitHub Actions API demand-poll query (docs/24
// §5.2): per runs listing — status=queued and status=in_progress (RUN-146) —
// then per-run jobs listings. The queued listing short-circuits on ETag; the
// in-progress listing is polled unconditionally, so the mock only ever 304s a
// conditional request on a listing it has served before.
type pollServer struct {
	mu           sync.Mutex
	runsRequests map[string]int // per runs-listing status ("queued", "in_progress")
	etagHits     map[string]int // conditional requests per listing status
	etagReplies  map[string]int // 304 responses served per listing status
	jobsRequests map[string]int
	queuedRuns   map[int64][]pollJob
	progressRuns map[int64][]pollJob
}

func setupPollServer(t *testing.T, queuedRuns, progressRuns map[int64][]pollJob) (*httptest.Server, *pollServer) {
	t.Helper()
	ps := &pollServer{
		runsRequests: make(map[string]int),
		etagHits:     make(map[string]int),
		etagReplies:  make(map[string]int),
		jobsRequests: make(map[string]int),
		queuedRuns:   queuedRuns,
		progressRuns: progressRuns,
	}
	mux := http.NewServeMux()

	// No per-request auth enforcement here; PAT auth is covered by the other
	// mock servers and polling reuses the same token resolution.
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		ps.mu.Lock()
		defer ps.mu.Unlock()
		ps.runsRequests[status]++
		conditional := r.Header.Get("If-None-Match") != ""
		if conditional {
			ps.etagHits[status]++
		}
		if conditional && ps.runsRequests[status] > 1 {
			// The run set has not changed since this listing's first poll.
			ps.etagReplies[status]++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"runnero-poll-etag-`+status+`"`)
		w.Header().Set("Content-Type", "application/json")
		runs := ps.queuedRuns
		if status == "in_progress" {
			runs = ps.progressRuns
		}
		runsList := make([]map[string]any, 0, len(runs))
		for id := range runs {
			runsList = append(runsList, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":   len(runsList),
			"workflow_runs": runsList,
		})
	})

	runIDs := make(map[int64]struct{})
	for id := range queuedRuns {
		runIDs[id] = struct{}{}
	}
	for id := range progressRuns {
		runIDs[id] = struct{}{}
	}
	for runID := range runIDs {
		runID := runID
		mux.HandleFunc(fmt.Sprintf("GET /repos/o/r/actions/runs/%d/jobs", runID), func(w http.ResponseWriter, r *http.Request) {
			ps.mu.Lock()
			ps.jobsRequests[fmt.Sprint(runID)]++
			ps.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			jobs := ps.queuedRuns[runID]
			if len(jobs) == 0 {
				jobs = ps.progressRuns[runID]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": len(jobs),
				"jobs":        jobs,
			})
		})
	}

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })
	return server, ps
}

type pollJob struct {
	ID     int64    `json:"id"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// The happy path counts only queued jobs whose labels the pool satisfies,
// across both listings (RUN-146): a job demanding unconfigured labels must
// not be counted (docs/24 §5.2), and neither must jobs that already started —
// including inside an in-progress run.
func TestGitHubPollQueuedJobsCountsMatchingQueuedJobs(t *testing.T) {
	server, _ := setupPollServer(t,
		map[int64][]pollJob{
			101: {
				{ID: 1, Status: "queued", Labels: []string{"self-hosted", "linux"}},
				{ID: 2, Status: "in_progress", Labels: []string{"self-hosted"}},
			},
			102: {
				{ID: 3, Status: "queued", Labels: []string{"gpu"}}, // unsatisfiable for the pool
			},
		},
		map[int64][]pollJob{
			301: {
				{ID: 4, Status: "in_progress", Labels: []string{"self-hosted"}}, // executing
				{ID: 5, Status: "queued", Labels: []string{"gpu"}},              // unsatisfiable for the pool
				{ID: 6, Status: "queued", Labels: []string{"self-hosted", "linux"}},
			},
		},
	)

	client, err := github.NewPATProvider("pat-token", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	queued, err := client.PollQueuedJobs(ctxTest(), provider.PollTarget{
		URL:    server.URL + "/o/r",
		Scope:  provider.ScopeRepo,
		Labels: `["self-hosted","linux"]`,
	})
	if err != nil {
		t.Fatalf("PollQueuedJobs failed: %v", err)
	}
	if queued != 2 {
		t.Fatalf("expected 2 countable queued jobs (one per listing), got %d", queued)
	}
}

// GitHub flips a run queued → in_progress as soon as any of its jobs starts,
// so a multi-job run whose other job is still queued never appears in the
// queued runs listing (RUN-146). The demand poll must count the surviving
// queued job from the in-progress listing or a pure on-demand pool starves.
func TestGitHubPollQueuedJobsCountsQueuedJobsInInProgressRuns(t *testing.T) {
	server, _ := setupPollServer(t,
		nil, // no queued runs — the run already flipped to in_progress
		map[int64][]pollJob{
			301: {
				{ID: 1, Status: "in_progress", Labels: []string{"self-hosted"}}, // executing
				{ID: 2, Status: "queued", Labels: []string{"self-hosted"}},      // the orphaned job
			},
		},
	)

	client, err := github.NewPATProvider("pat-token", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	queued, err := client.PollQueuedJobs(ctxTest(), provider.PollTarget{
		URL:    server.URL + "/o/r",
		Scope:  provider.ScopeRepo,
		Labels: `["self-hosted"]`,
	})
	if err != nil {
		t.Fatalf("PollQueuedJobs failed: %v", err)
	}
	if queued != 1 {
		t.Fatalf("expected the queued job inside the in-progress run to be counted (got %d)", queued)
	}
}

// The queued listing is answered from the 304 replay while its run set is
// unchanged (docs/24 §5.2/§5.4). The in-progress listing is deliberately
// never conditional (RUN-146): jobs inside an in-progress run transition
// without changing the run set, so a cached count there could go stale.
func TestGitHubPollQueuedJobsETagShortCircuit(t *testing.T) {
	server, ps := setupPollServer(t,
		map[int64][]pollJob{
			201: {{ID: 1, Status: "queued", Labels: []string{"self-hosted"}}},
		},
		nil,
	)

	client, err := github.NewPATProvider("pat-token", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}
	target := provider.PollTarget{URL: server.URL + "/o/r", Scope: provider.ScopeRepo, Labels: `["self-hosted"]`}

	for i := 0; i < 3; i++ {
		queued, err := client.PollQueuedJobs(ctxTest(), target)
		if err != nil {
			t.Fatalf("PollQueuedJobs iteration %d failed: %v", i, err)
		}
		if queued != 1 {
			t.Fatalf("iteration %d: expected cached count 1, got %d", i, queued)
		}
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.runsRequests["queued"] != 3 || ps.etagHits["queued"] != 2 || ps.etagReplies["queued"] != 2 {
		t.Errorf("queued listing: expected 3 requests with 2 conditional hits/replies, got %d/%d/%d",
			ps.runsRequests["queued"], ps.etagHits["queued"], ps.etagReplies["queued"])
	}
	if ps.runsRequests["in_progress"] != 3 || ps.etagHits["in_progress"] != 0 {
		t.Errorf("in-progress listing: expected 3 unconditional requests, got %d requests / %d conditional",
			ps.runsRequests["in_progress"], ps.etagHits["in_progress"])
	}
	if ps.jobsRequests["201"] != 1 {
		t.Errorf("expected jobs listing only on the cold poll, got %d requests", ps.jobsRequests["201"])
	}
}

// The ETag cache key includes the pool's label contract: two pools polling
// the same repo with different labels must not replay each other's counts.
func TestGitHubPollQueuedJobsCacheKeyIncludesPoolLabels(t *testing.T) {
	server, ps := setupPollServer(t,
		map[int64][]pollJob{
			401: {
				{ID: 1, Status: "queued", Labels: []string{"linux"}},
				{ID: 2, Status: "queued", Labels: []string{"linux"}},
			},
		},
		nil,
	)

	client, err := github.NewPATProvider("pat-token", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	linuxCount, err := client.PollQueuedJobs(ctxTest(), provider.PollTarget{
		URL: server.URL + "/o/r", Scope: provider.ScopeRepo, Labels: `["linux"]`,
	})
	if err != nil {
		t.Fatalf("PollQueuedJobs (linux pool) failed: %v", err)
	}
	if linuxCount != 2 {
		t.Fatalf("linux pool: expected 2, got %d", linuxCount)
	}

	gpuCount, err := client.PollQueuedJobs(ctxTest(), provider.PollTarget{
		URL: server.URL + "/o/r", Scope: provider.ScopeRepo, Labels: `["gpu"]`,
	})
	if err != nil {
		t.Fatalf("PollQueuedJobs (gpu pool) failed: %v", err)
	}
	if gpuCount != 0 {
		t.Fatalf("gpu pool: expected a fresh count of 0, got %d (replayed the linux pool's cache?)", gpuCount)
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.etagHits["queued"] != 0 {
		t.Errorf("the gpu pool's cold poll must not send If-None-Match, got %d conditional requests", ps.etagHits["queued"])
	}
}

// Org- and global-scoped targets keep webhook-only demand: the scope guard
// rejects without any network call (docs/24 §5.2).
func TestGitHubPollQueuedJobsRejectsOrgAndGlobalScopes(t *testing.T) {
	server, ps := setupPollServer(t, nil, nil)

	client, err := github.NewPATProvider("pat-token", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	for _, scope := range []provider.RegistrationScope{provider.ScopeOrg, provider.ScopeGlobal} {
		if _, err := client.PollQueuedJobs(ctxTest(), provider.PollTarget{URL: server.URL + "/o", Scope: scope}); !errors.Is(err, provider.ErrPollingScopeUnsupported) {
			t.Errorf("scope %q: expected ErrPollingScopeUnsupported, got %v", scope, err)
		}
	}
	if _, err := client.PollQueuedJobs(ctxTest(), provider.PollTarget{URL: server.URL + "/o", Scope: ""}); !errors.Is(err, provider.ErrPollingScopeUnsupported) {
		t.Errorf("empty scope: expected ErrPollingScopeUnsupported, got %v", err)
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.runsRequests) != 0 {
		t.Errorf("scope guard must not touch the network, got %d runs requests", len(ps.runsRequests))
	}
}

// HTTP failures map to errors (docs/24 §5.10).
func TestGitHubPollQueuedJobsMapsHTTPErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	client, err := github.NewPATProvider("bad-token", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	_, err = client.PollQueuedJobs(ctxTest(), provider.PollTarget{URL: server.URL + "/o/r", Scope: provider.ScopeRepo})
	if err == nil {
		t.Fatal("expected an error for the 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error message, got %v", err)
	}
}

// ctxTest returns a background context; kept as a helper so future deadline
// injection is a one-line change across the polling tests.
func ctxTest() context.Context { return context.Background() }
