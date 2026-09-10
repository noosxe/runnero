package github_test

import (
	"context"
	"fmt"
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

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// TestRunnerLatestJobsRepoScope: repo-scoped runners resolve jobs by scanning
// the repo's newest workflow runs and matching each run's jobs on runner_id,
// with the same Bearer credential the listing already uses (docs/21 §5.3).
// The GitHub API has no recent-jobs-per-runner endpoint.
func TestRunnerLatestJobsRepoScope(t *testing.T) {
	var gotRunsQuery, gotJobsQuery, gotJobsPath, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/my-org/my-repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		gotRunsQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		writeJSON(w, `{"total_count": 2, "workflow_runs": [{"id": 555002}, {"id": 555001}]}`)
	})
	mux.HandleFunc("/repos/my-org/my-repo/actions/runs/555002/jobs", func(w http.ResponseWriter, r *http.Request) {
		gotJobsPath = r.URL.Path
		gotJobsQuery = r.URL.RawQuery
		// Job 9100 belongs to a different runner and must be filtered out.
		writeJSON(w, `{"total_count": 2, "jobs": [
			{"id": 9001, "runner_id": 7001, "conclusion": "success", "completed_at": "2026-09-08T07:15:00Z"},
			{"id": 9100, "runner_id": 9999, "conclusion": "failure", "completed_at": "2026-09-08T07:16:00Z"}
		]}`)
	})
	mux.HandleFunc("/repos/my-org/my-repo/actions/runs/555001/jobs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"total_count": 1, "jobs": [
			{"id": 9000, "runner_id": 7001, "conclusion": null, "completed_at": null}
		]}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	jobs, err := newJobsTestClient(t, server.URL).RunnerLatestJobs(
		context.Background(), provider.ScopeRepo, "https://github.com/my-org/my-repo", 7001)
	if err != nil {
		t.Fatalf("RunnerLatestJobs failed: %v", err)
	}

	if gotRunsQuery != "per_page=10" {
		t.Errorf("unexpected runs query: %s", gotRunsQuery)
	}
	if gotJobsPath != "/repos/my-org/my-repo/actions/runs/555002/jobs" || gotJobsQuery != "per_page=100" {
		t.Errorf("unexpected jobs request: %s?%s", gotJobsPath, gotJobsQuery)
	}
	if gotAuth != "Bearer valid-pat-secret" {
		t.Errorf("unexpected Authorization header: %q", gotAuth)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs (other runners filtered out), got %d: %+v", len(jobs), jobs)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-08T07:15:00Z")
	// Newest first: the concluded job sorts ahead of the unconcluded one.
	if jobs[0].ID != 9001 || jobs[0].Conclusion != "success" || !jobs[0].CompletedAt.Equal(want) {
		t.Errorf("jobs[0] = %+v, want {9001 success %v}", jobs[0], want)
	}
	if jobs[1].ID != 9000 || jobs[1].Conclusion != "" || !jobs[1].CompletedAt.IsZero() {
		t.Errorf("unconcluded job must carry empty conclusion and zero time, got %+v", jobs[1])
	}
}

// TestRunnerLatestJobsOrgScope: org-scoped runner ids resolve against the org
// runs and org per-run jobs endpoints (docs/21 §5.3).
func TestRunnerLatestJobsOrgScope(t *testing.T) {
	var gotJobsPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/my-org/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"total_count": 1, "workflow_runs": [{"id": 555003}]}`)
	})
	mux.HandleFunc("/orgs/my-org/actions/runs/555003/jobs", func(w http.ResponseWriter, r *http.Request) {
		gotJobsPath = r.URL.Path
		writeJSON(w, `{"total_count": 1, "jobs": [{"id": 9003, "runner_id": 7002, "conclusion": "failure", "completed_at": "2026-09-08T07:20:00Z"}]}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	jobs, err := newJobsTestClient(t, server.URL).RunnerLatestJobs(
		context.Background(), provider.ScopeOrg, "https://github.com/my-org", 7002)
	if err != nil {
		t.Fatalf("RunnerLatestJobs failed: %v", err)
	}
	if gotJobsPath != "/orgs/my-org/actions/runs/555003/jobs" {
		t.Errorf("unexpected jobs request path: %s", gotJobsPath)
	}
	if len(jobs) != 1 || jobs[0].Conclusion != "failure" {
		t.Errorf("jobs = %+v, want one failure job", jobs)
	}
}

// TestRunnerLatestJobsUnsupportedScopes: enterprise runners have no public
// runs listing, and a missing runner id cannot be keyed — both must error so
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
	mux.HandleFunc("/repos/my-org/my-repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
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

// TestRunnerLatestJobsScanWindow: enrichment stops once it has enough of the
// runner's jobs, bounding the API calls it makes.
func TestRunnerLatestJobsScanWindow(t *testing.T) {
	runs := 20
	jobsFetched := map[int64]bool{}
	mux := http.NewServeMux()
	for i := 1; i <= runs; i++ {
		runID := int64(560000 + i)
		mux.HandleFunc(fmt.Sprintf("/repos/my-org/my-repo/actions/runs/%d/jobs", runID), func(w http.ResponseWriter, r *http.Request) {
			jobsFetched[runID] = true
			writeJSON(w, fmt.Sprintf(`{"total_count": 1, "jobs": [{"id": %d, "runner_id": 7005, "conclusion": "success", "completed_at": "2026-09-08T07:%02d:00Z"}]}`, runID, i%60))
		})
	}
	mux.HandleFunc("/repos/my-org/my-repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		sb.WriteString(`{"total_count": 20, "workflow_runs": [`)
		for i := 1; i <= runs; i++ {
			if i > 1 {
				sb.WriteString(",")
			}
			_, _ = fmt.Fprintf(&sb, `{"id": %d}`, 560000+i)
		}
		sb.WriteString(`]}`)
		writeJSON(w, sb.String())
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	jobs, err := newJobsTestClient(t, server.URL).RunnerLatestJobs(
		context.Background(), provider.ScopeRepo, "https://github.com/my-org/my-repo", 7005)
	if err != nil {
		t.Fatalf("RunnerLatestJobs failed: %v", err)
	}
	if len(jobs) != 3 {
		t.Errorf("expected the scan to stop at 3 jobs, got %d", len(jobs))
	}
	if len(jobsFetched) > 5 {
		t.Errorf("expected at most 5 runs scanned, fetched %d", len(jobsFetched))
	}
}
