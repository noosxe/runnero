package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/provider/github"
)

// runnersPayload builds a GitHub list-runners JSON body from (name, busy, online) tuples.
func runnersPayload(t *testing.T, runners [][3]any) string {
	t.Helper()
	type ghRunner struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Busy   bool   `json:"busy"`
		Status string `json:"status"`
		OS     string `json:"os"`
	}
	resp := struct {
		TotalCount int        `json:"total_count"`
		Runners    []ghRunner `json:"runners"`
	}{TotalCount: len(runners)}
	for i, r := range runners {
		busy := r[1].(bool)
		online := r[2].(bool)
		status := "offline"
		if online {
			status = "online"
		}
		resp.Runners = append(resp.Runners, ghRunner{
			ID:     int64(1000 + i),
			Name:   r[0].(string),
			Busy:   busy,
			Status: status,
			OS:     "linux",
		})
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshalling runners payload: %v", err)
	}
	return string(b)
}

func assertRunnersEqual(t *testing.T, got []provider.RemoteRunnerStatus, want []provider.RemoteRunnerStatus) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d runners, got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("runner[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListRunnersRepoScope(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/my-org/my-repo/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(runnersPayload(t, [][3]any{
			{"runnero-busy", true, true},
			{"runnero-idle", false, true},
			{"runnero-offline", false, false},
		})))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	client, err := github.NewPATProvider("valid-pat-secret", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	runners, err := client.ListRunners(context.Background(), provider.ScopeRepo, "https://github.com/my-org/my-repo")
	if err != nil {
		t.Fatalf("ListRunners failed: %v", err)
	}

	if gotPath != "/repos/my-org/my-repo/actions/runners" {
		t.Errorf("unexpected request path: %s", gotPath)
	}
	if gotQuery != "per_page=100&page=1" {
		t.Errorf("unexpected query: %s", gotQuery)
	}
	if gotAuth != "Bearer valid-pat-secret" {
		t.Errorf("unexpected Authorization header: %q", gotAuth)
	}

	assertRunnersEqual(t, runners, []provider.RemoteRunnerStatus{
		{Name: "runnero-busy", Busy: true, Online: true},
		{Name: "runnero-idle", Busy: false, Online: true},
		{Name: "runnero-offline", Busy: false, Online: false},
	})
}

func TestListRunnersOrgAndEnterpriseScopes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/my-org/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(runnersPayload(t, [][3]any{{"org-runner", true, true}})))
	})
	mux.HandleFunc("/enterprises/my-ent/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(runnersPayload(t, [][3]any{{"ent-runner", false, true}})))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	client, err := github.NewPATProvider("valid-pat-secret", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}
	ctx := context.Background()

	orgRunners, err := client.ListRunners(ctx, provider.ScopeOrg, "https://github.com/my-org")
	if err != nil {
		t.Fatalf("ListRunners (org) failed: %v", err)
	}
	assertRunnersEqual(t, orgRunners, []provider.RemoteRunnerStatus{{Name: "org-runner", Busy: true, Online: true}})

	entRunners, err := client.ListRunners(ctx, provider.ScopeGlobal, "https://github.com/my-ent")
	if err != nil {
		t.Fatalf("ListRunners (enterprise) failed: %v", err)
	}
	assertRunnersEqual(t, entRunners, []provider.RemoteRunnerStatus{{Name: "ent-runner", Busy: false, Online: true}})
}

func TestListRunnersPagination(t *testing.T) {
	pagesRequested := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/my-org/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		pagesRequested++
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" {
			// Full page of 100 forces a second request.
			runners := make([][3]any, 0, 100)
			for i := 0; i < 100; i++ {
				runners = append(runners, [3]any{fmt.Sprintf("runner-%03d", i), false, true})
			}
			_, _ = w.Write([]byte(runnersPayload(t, runners)))
			return
		}
		_, _ = w.Write([]byte(runnersPayload(t, [][3]any{{"tail-runner", true, true}})))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	client, err := github.NewPATProvider("valid-pat-secret", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}

	runners, err := client.ListRunners(context.Background(), provider.ScopeOrg, "https://github.com/my-org")
	if err != nil {
		t.Fatalf("ListRunners failed: %v", err)
	}

	if pagesRequested != 2 {
		t.Errorf("expected 2 pages requested, got %d", pagesRequested)
	}
	if len(runners) != 101 {
		t.Fatalf("expected 101 runners across pages, got %d", len(runners))
	}
	last := runners[len(runners)-1]
	if last.Name != "tail-runner" || !last.Busy {
		t.Errorf("unexpected tail runner: %+v", last)
	}
}

func TestListRunnersRepoScopeRequiresRepoName(t *testing.T) {
	client, err := github.NewPATProvider("valid-pat-secret")
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}
	_, err = client.ListRunners(context.Background(), provider.ScopeRepo, "https://github.com/my-org")
	if err == nil {
		t.Fatal("expected error for repo scope without repository name")
	}
	if !strings.Contains(err.Error(), "repository name required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListRunnersAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/my-org/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	client, err := github.NewPATProvider("valid-pat-secret", github.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPATProvider failed: %v", err)
	}
	_, err = client.ListRunners(context.Background(), provider.ScopeOrg, "https://github.com/my-org")
	if err == nil {
		t.Fatal("expected error for API failure")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("unexpected error: %v", err)
	}
}
