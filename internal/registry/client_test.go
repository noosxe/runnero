package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantReg   string
		wantRepo  string
		wantTag   string
		wantInsec bool
		wantErr   bool
	}{
		{
			name:     "ghcr with tag",
			input:    "ghcr.io/noosxe/runnero:latest",
			wantReg:  "ghcr.io",
			wantRepo: "noosxe/runnero",
			wantTag:  "latest",
		},
		{
			name:     "ghcr with version tag",
			input:    "ghcr.io/noosxe/runnero:v1.2.3",
			wantReg:  "ghcr.io",
			wantRepo: "noosxe/runnero",
			wantTag:  "v1.2.3",
		},
		{
			name:     "docker hub official unqualified",
			input:    "ubuntu",
			wantReg:  "registry-1.docker.io",
			wantRepo: "library/ubuntu",
			wantTag:  "latest",
		},
		{
			name:     "docker hub official with tag",
			input:    "ubuntu:24.04",
			wantReg:  "registry-1.docker.io",
			wantRepo: "library/ubuntu",
			wantTag:  "24.04",
		},
		{
			name:     "docker hub user repo",
			input:    "myuser/custom-runner:beta",
			wantReg:  "registry-1.docker.io",
			wantRepo: "myuser/custom-runner",
			wantTag:  "beta",
		},
		{
			name:      "localhost insecure",
			input:     "localhost:5000/test-img:v1",
			wantReg:   "localhost:5000",
			wantRepo:  "test-img",
			wantTag:   "v1",
			wantInsec: true,
		},
		{
			name:      "http prefix insecure",
			input:     "http://127.0.0.1:8080/runner:dev",
			wantReg:   "127.0.0.1:8080",
			wantRepo:  "runner",
			wantTag:   "dev",
			wantInsec: true,
		},
		{
			name:    "empty image",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseImageRef(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseImageRef() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Registry != tt.wantReg {
				t.Errorf("Registry = %q, want %q", got.Registry, tt.wantReg)
			}
			if got.Repository != tt.wantRepo {
				t.Errorf("Repository = %q, want %q", got.Repository, tt.wantRepo)
			}
			if got.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", got.Tag, tt.wantTag)
			}
			if got.Insecure != tt.wantInsec {
				t.Errorf("Insecure = %v, want %v", got.Insecure, tt.wantInsec)
			}
		})
	}
}

func TestGetRemoteImageDigest_WithHeader(t *testing.T) {
	expectedDigest := "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/myorg/myimage/manifests/v1.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", expectedDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse srv url: %v", err)
	}

	client := NewClient(WithHTTPClient(srv.Client()))
	digest, err := client.GetRemoteImageDigest(context.Background(), fmt.Sprintf("http://%s/myorg/myimage:v1.0", u.Host))
	if err != nil {
		t.Fatalf("GetRemoteImageDigest failed: %v", err)
	}
	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestGetRemoteImageDigest_CalculateBodyDigest(t *testing.T) {
	manifestJSON := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"sha256:123"}}`
	sum := sha256.Sum256([]byte(manifestJSON))
	expectedDigest := fmt.Sprintf("sha256:%x", sum)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// Do not return Docker-Content-Digest header on HEAD to test GET fallback
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", DockerManifestV2)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(manifestJSON))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := NewClient(WithHTTPClient(srv.Client()))
	digest, err := client.GetRemoteImageDigest(context.Background(), fmt.Sprintf("http://%s/test/pkg:latest", u.Host))
	if err != nil {
		t.Fatalf("GetRemoteImageDigest failed: %v", err)
	}
	if digest != expectedDigest {
		t.Errorf("got %q, want %q", digest, expectedDigest)
	}
}

func TestGetRemoteImageDigest_AuthChallengeBearer(t *testing.T) {
	expectedDigest := "sha256:c0ffee1234567890abcdef0123456789c0ffee1234567890abcdef0123456789"
	var tokenIssued bool

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenIssued = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token": "mock-bearer-token-12345"}`))
		case "/v2/secure/runner/manifests/latest":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer mock-bearer-token-12345" {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="mock-reg",scope="repository:secure/runner:pull"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", expectedDigest)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := NewClient(WithHTTPClient(srv.Client()))
	digest, err := client.GetRemoteImageDigest(context.Background(), fmt.Sprintf("http://%s/secure/runner:latest", u.Host))
	if err != nil {
		t.Fatalf("GetRemoteImageDigest failed: %v", err)
	}
	if !tokenIssued {
		t.Errorf("expected bearer token to have been issued")
	}
	if digest != expectedDigest {
		t.Errorf("got %q, want %q", digest, expectedDigest)
	}
}

func TestGetRemoteImageDigest_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	client := NewClient(WithHTTPClient(srv.Client()))
	_, err := client.GetRemoteImageDigest(context.Background(), fmt.Sprintf("http://%s/missing/runner:latest", u.Host))
	if err == nil {
		t.Fatalf("expected error for HTTP 404, got nil")
	}
}
