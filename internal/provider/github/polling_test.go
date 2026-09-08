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

// pollServer simulates the GitHub Actions API two-step demand-poll query
// (docs/24 §5.2): queued runs listing, then per-run jobs listings.
type pollServer struct {
	mu           sync.Mutex
	runsRequests int
	etagHits     int // requests carrying If-None-Match
	etagReplies  int // 304 responses served
	jobsRequests map[string]int
}

func setupPollServer(t *testing.T, runs map[int64][]pollJob) (*httptest.Server, *pollServer) {
	t.Helper()
	ps := &pollServer{jobsRequests: make(map[string]int)}
	mux := http.NewServeMux()

	// No per-request auth enforcement here; PAT auth is covered by the other
	// mock servers and polling reuses the same token resolution.
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		ps.runsRequests++
		if r.Header.Get("If-None-Match") != "" {
			ps.etagHits++
		}
		if ps.runsRequests > 1 {
			// The queued-runs set has not changed since the first poll.
			ps.etagReplies++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"runnero-poll-etag"`)
		w.Header().Set("Content-Type", "application/json")
		runsList := make([]map[string]any, 0, len(runs))
		for id := range runs {
			runsList = append(runsList, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count":   len(runsList),
			"workflow_runs": runsList,
		})
	})

	for runID := range runs {
		runID := runID
		mux.HandleFunc(fmt.Sprintf("GET /repos/o/r/actions/runs/%d/jobs", runID), func(w http.ResponseWriter, r *http.Request) {
			ps.mu.Lock()
			ps.jobsRequests[fmt.Sprint(runID)]++
			ps.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": len(runs[runID]),
				"jobs":        runs[runID],
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

// The happy path counts only queued jobs whose labels the pool satisfies:
// a job demanding unconfigured labels must not be counted (docs/24 §5.2),
// and neither must jobs that already started.
func TestGitHubPollQueuedJobsCountsMatchingQueuedJobs(t *testing.T) {
	server, _ := setupPollServer(t, map[int64][]pollJob{
		101: {
			{ID: 1, Status: "queued", Labels: []string{"self-hosted", "linux"}},
			{ID: 2, Status: "in_progress", Labels: []string{"self-hosted"}},
		},
		102: {
			{ID: 3, Status: "queued", Labels: []string{"gpu"}}, // unsatisfiable for the pool
		},
	})

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
	if queued != 1 {
		t.Fatalf("expected 1 countable queued job, got %d", queued)
	}
}

// A second poll with an unchanged runs listing is answered from the 304 replay
// without touching the jobs listings (docs/24 §5.2/§5.4).
func TestGitHubPollQueuedJobsETagShortCircuit(t *testing.T) {
	server, ps := setupPollServer(t, map[int64][]pollJob{
		201: {{ID: 1, Status: "queued", Labels: []string{"self-hosted"}}},
	})

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
	if ps.runsRequests != 3 || ps.etagHits != 2 || ps.etagReplies != 2 {
		t.Errorf("expected 3 runs requests with 2 conditional hits/replies, got %d/%d/%d",
			ps.runsRequests, ps.etagHits, ps.etagReplies)
	}
	if ps.jobsRequests["201"] != 1 {
		t.Errorf("expected jobs listing only on the cold poll, got %d requests", ps.jobsRequests["201"])
	}
}

// Org- and global-scoped targets keep webhook-only demand: the scope guard
// rejects without any network call (docs/24 §5.2).
func TestGitHubPollQueuedJobsRejectsOrgAndGlobalScopes(t *testing.T) {
	server, ps := setupPollServer(t, nil)

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
	if ps.runsRequests != 0 {
		t.Errorf("scope guard must not touch the network, got %d runs requests", ps.runsRequests)
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
