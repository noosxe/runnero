package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout for registry HTTP requests.
const DefaultTimeout = 15 * time.Second

// Accepted manifest media types for Docker Registry v2 / OCI Distribution Spec.
const (
	DockerManifestV2     = "application/vnd.docker.distribution.manifest.v2+json"
	DockerManifestListV2 = "application/vnd.docker.distribution.manifest.list.v2+json"
	OCIManifestV1        = "application/vnd.oci.image.manifest.v1+json"
	OCIIndexV1           = "application/vnd.oci.image.index.v1+json"
)

var acceptHeader = strings.Join([]string{
	DockerManifestV2,
	DockerManifestListV2,
	OCIManifestV1,
	OCIIndexV1,
}, ", ")

// ParsedReference represents the parsed components of an OCI / Docker image reference.
type ParsedReference struct {
	Registry   string // e.g. "ghcr.io", "registry-1.docker.io", "localhost:5000"
	Repository string // e.g. "noosxe/runnero", "library/ubuntu"
	Tag        string // e.g. "latest", "v1.0.0"
	Insecure   bool   // true for localhost/127.0.0.1 or explicit http endpoints
}

// ParseImageRef parses a docker image string into its registry, repository, and tag components.
func ParseImageRef(imageRef string) (ParsedReference, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return ParsedReference{}, errors.New("image reference cannot be empty")
	}

	var insec bool
	if strings.HasPrefix(imageRef, "http://") {
		insec = true
		imageRef = strings.TrimPrefix(imageRef, "http://")
	} else if strings.HasPrefix(imageRef, "https://") {
		imageRef = strings.TrimPrefix(imageRef, "https://")
	}

	tag := "latest"
	repoPart := imageRef

	// If there is a digest (@sha256:...), separate it
	if atIdx := strings.Index(repoPart, "@"); atIdx != -1 {
		tag = repoPart[atIdx+1:]
		repoPart = repoPart[:atIdx]
	} else if colonIdx := strings.LastIndex(repoPart, ":"); colonIdx != -1 {
		slashIdx := strings.Index(repoPart, "/")
		if slashIdx == -1 || colonIdx > slashIdx {
			tag = repoPart[colonIdx+1:]
			repoPart = repoPart[:colonIdx]
		}
	}

	parts := strings.Split(repoPart, "/")
	var registry, repository string

	if len(parts) == 1 {
		// Single word e.g. "ubuntu" -> Docker Hub official repository "library/ubuntu"
		registry = "registry-1.docker.io"
		repository = "library/" + parts[0]
	} else {
		first := parts[0]
		if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
			registry = first
			repository = strings.Join(parts[1:], "/")
		} else {
			// Docker Hub user repository e.g. "myuser/myimage"
			registry = "registry-1.docker.io"
			repository = strings.Join(parts, "/")
		}
	}

	if strings.HasPrefix(registry, "localhost") || strings.HasPrefix(registry, "127.0.0.1") {
		insec = true
	}

	return ParsedReference{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Insecure:   insec,
	}, nil
}

// Client interacts with OCI / Docker v2 registries to inspect images and fetch digests.
type Client struct {
	httpClient *http.Client
}

// Option configures Client.
type Option func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// NewClient creates a new registry client.
func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// GetRemoteImageDigest retrieves the content digest (e.g. "sha256:...") for an image from its remote registry.
func (c *Client) GetRemoteImageDigest(ctx context.Context, imageRef string) (string, error) {
	ref, err := ParseImageRef(imageRef)
	if err != nil {
		return "", err
	}

	scheme := "https"
	if ref.Insecure {
		scheme = "http"
	}

	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, ref.Registry, ref.Repository, ref.Tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", acceptHeader)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching manifest head: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var authToken string
	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("Www-Authenticate")
		token, err := c.resolveAuthToken(ctx, authHeader, ref.Repository)
		if err != nil {
			return "", fmt.Errorf("registry auth failed: %w", err)
		}
		authToken = token

		req, err = http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", acceptHeader)
		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("fetching authenticated manifest head: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("image %s not found in registry (HTTP 404)", imageRef)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d from registry for %s", resp.StatusCode, imageRef)
	}

	if digest := resp.Header.Get("Docker-Content-Digest"); digest != "" {
		return digest, nil
	}

	// If HEAD response omitted Docker-Content-Digest, perform GET to calculate sha256 of manifest body
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	getReq.Header.Set("Accept", acceptHeader)
	if authToken != "" {
		getReq.Header.Set("Authorization", "Bearer "+authToken)
	}

	getResp, err := c.httpClient.Do(getReq)
	if err != nil {
		return "", fmt.Errorf("fetching manifest body: %w", err)
	}
	defer func() { _ = getResp.Body.Close() }()

	if getResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d fetching manifest body", getResp.StatusCode)
	}

	if digest := getResp.Header.Get("Docker-Content-Digest"); digest != "" {
		return digest, nil
	}

	body, err := io.ReadAll(io.LimitReader(getResp.Body, 4*1024*1024))
	if err != nil {
		return "", fmt.Errorf("reading manifest body: %w", err)
	}

	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func (c *Client) resolveAuthToken(ctx context.Context, authHeader, repository string) (string, error) {
	if authHeader == "" {
		return "", errors.New("missing Www-Authenticate header")
	}

	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return "", fmt.Errorf("unsupported auth challenge: %s", authHeader)
	}

	challenge := strings.TrimPrefix(authHeader, authHeader[:7])
	params := parseChallengeParams(challenge)

	realm := params["realm"]
	if realm == "" {
		return "", errors.New("missing realm in Www-Authenticate header")
	}

	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("invalid realm URL %q: %w", realm, err)
	}

	q := u.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	if scope := params["scope"]; scope != "" {
		q.Set("scope", scope)
	} else if repository != "" {
		q.Set("scope", "repository:"+repository+":pull")
	}
	u.RawQuery = q.Encode()

	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}

	tokenResp, err := c.httpClient.Do(tokenReq)
	if err != nil {
		return "", fmt.Errorf("requesting bearer token from %s: %w", u.String(), err)
	}
	defer func() { _ = tokenResp.Body.Close() }()

	if tokenResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned status %d", tokenResp.StatusCode)
	}

	var tokenBody struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenBody); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}

	if tokenBody.Token != "" {
		return tokenBody.Token, nil
	}
	if tokenBody.AccessToken != "" {
		return tokenBody.AccessToken, nil
	}

	return "", errors.New("token endpoint returned empty token")
}

func parseChallengeParams(s string) map[string]string {
	res := make(map[string]string)
	var current strings.Builder
	inQuotes := false

	parts := make([]string, 0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inQuotes = !inQuotes
		} else if ch == ',' && !inQuotes {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if eq := strings.Index(p, "="); eq != -1 {
			k := strings.TrimSpace(p[:eq])
			v := strings.Trim(strings.TrimSpace(p[eq+1:]), `"`)
			res[k] = v
		}
	}
	return res
}
