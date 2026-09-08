package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/provider/github"
)

func newJobsTestClient(t *testing.T, serverURL string) *github.Client {
	t.Helper()
	client, err := github.NewPATProvider("valid-pat-secret", github.WithBaseURL(serverURL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}
	return client
}

// TestRunnerLatestJobsRepoScope: repo-scoped runners resolve jobs from the
// repo's per-runner jobs endpoint with the same Bearer credential the listing
// already uses (docs/21 §5.3).
func TestRunnerLatestJobsRepoScope(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/my-org/my-repo/actions/runners/7001/jobs", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"jobs": [
				{"id": 9001, "conclusion": "success", "completed_at": "2026-09-08T07:15:00Z"},
				{"id": 9000, "conclusion": null, "completed_at": null}
			]
		}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	jobs, err := newJobsTestClient(t, server.URL).RunnerLatestJobs(
		context.Background(), provider.ScopeRepo, "https://github.com/my-org/my-repo", 7001)
	if err != nil {
		t.Fatalf("RunnerLatestJobs failed: %v", err)
	}

	if gotPath != "/repos/my-org/my-repo/actions/runners/7001/jobs" {
		t.Errorf("unexpected request path: %s", gotPath)
	}
	if gotQuery != "per_page=10" {
		t.Errorf("unexpected query: %s", gotQuery)
	}
	if gotAuth != "Bearer valid-pat-secret" {
		t.Errorf("unexpected Authorization header: %q", gotAuth)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-08T07:15:00Z")
	if jobs[0].ID != 9001 || jobs[0].Conclusion != "success" || !jobs[0].CompletedAt.Equal(want) {
		t.Errorf("jobs[0] = %+v, want {9001 success %v}", jobs[0], want)
	}
	if jobs[1].ID != 9000 || jobs[1].Conclusion != "" || !jobs[1].CompletedAt.IsZero() {
		t.Errorf("unconcluded job must carry empty conclusion and zero time, got %+v", jobs[1])
	}
}

// TestRunnerLatestJobsOrgScope: org-scoped runner ids resolve against the
// org endpoint (docs/21 §5.3).
func TestRunnerLatestJobsOrgScope(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/my-org/actions/runners/7002/jobs", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"total_count": 1, "jobs": [{"id": 9003, "conclusion": "failure", "completed_at": "2026-09-08T07:20:00Z"}]}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	jobs, err := newJobsTestClient(t, server.URL).RunnerLatestJobs(
		context.Background(), provider.ScopeOrg, "https://github.com/my-org", 7002)
	if err != nil {
		t.Fatalf("RunnerLatestJobs failed: %v", err)
	}
	if gotPath != "/orgs/my-org/actions/runners/7002/jobs" {
		t.Errorf("unexpected request path: %s", gotPath)
	}
	if len(jobs) != 1 || jobs[0].Conclusion != "failure" {
		t.Errorf("jobs = %+v, want one failure job", jobs)
	}
}

// TestRunnerLatestJobsUnsupportedScopes: enterprise runners have no public
// jobs endpoint, and a missing runner id cannot be keyed — both must error so
// callers fail open (docs/21 §5.3).
func TestRunnerLatestJobsUnsupportedScopes(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	t.Cleanup(func() { server.Close() })
	client := newJobsTestClient(t, server.URL)
	ctx := context.Background()

	if _, err := client.RunnerLatestJobs(ctx, provider.ScopeGlobal, "https://github.com/my-ent", 7003); err == nil {
		t.Error("enterprise scope must be unsupported")
	} else if !strings.Contains(err.Error(), "enterprise") {
		t.Errorf("unexpected error: %v", err)
	}

	if _, err := client.RunnerLatestJobs(ctx, provider.ScopeRepo, "https://github.com/my-org/my-repo", 0); err == nil {
		t.Error("zero runner id must error")
	}
}

// TestRunnerLatestJobsAPIError: non-200 responses surface as errors so the
// orchestrator degrades the row to completed (docs/21 G3).
func TestRunnerLatestJobsAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/my-org/my-repo/actions/runners/7004/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	if _, err := newJobsTestClient(t, server.URL).RunnerLatestJobs(
		context.Background(), provider.ScopeRepo, "https://github.com/my-org/my-repo", 7004); err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}
