package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/provider"
)

var (
	// ErrInvalidTargetURL is returned when the target URL cannot be parsed.
	ErrInvalidTargetURL = errors.New("invalid Forgejo target URL")
	// ErrEmptyTokenReceived is returned when the API responds with an empty runner token.
	ErrEmptyTokenReceived = errors.New("empty runner registration token received from Forgejo API")
)

// Client implements provider.GitProvider and provider.RenovateTokenProvider for Forgejo.
type Client struct {
	baseURL    string
	pat        string
	httpClient *http.Client

	mu sync.RWMutex
}

// ClientOption configures a Forgejo Client.
type ClientOption func(*Client)

// WithBaseURL sets the base URL for the Forgejo instance (e.g., https://forgejo.example.com).
func WithBaseURL(rawURL string) ClientOption {
	return func(c *Client) {
		if rawURL != "" {
			c.baseURL = strings.TrimRight(rawURL, "/")
		}
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// NewClient creates a new Forgejo provider client with the given PAT.
func NewClient(pat string, opts ...ClientOption) (*Client, error) {
	if strings.TrimSpace(pat) == "" {
		return nil, fmt.Errorf("%w: PAT cannot be empty", provider.ErrMissingCredentials)
	}

	c := &Client{
		pat:        strings.TrimSpace(pat),
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: provider.NewRateLimitTransport("forgejo", http.DefaultTransport)},
	}

	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// ScalingMode returns ScalingPolling because Forgejo lacks workflow_job webhooks.
func (c *Client) ScalingMode() provider.ScalingMode {
	return provider.ScalingPolling
}

// ValidateCredentials checks that the PAT is valid against the Forgejo instance.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	c.mu.RLock()
	baseURL := c.baseURL
	c.mu.RUnlock()

	// If no base URL is configured yet, format check passes.
	if baseURL == "" {
		return nil
	}

	endpoint := baseURL + "/api/v1/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	c.setAuthHeader(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling Forgejo API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("forgejo credentials validation failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// GetRegistrationToken retrieves a short-lived Actions runner registration token from Forgejo.
func (c *Client) GetRegistrationToken(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
	instanceURL, owner, repo, err := parseForgejoTargetURL(targetURL)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	if c.baseURL == "" && instanceURL != "" {
		c.baseURL = instanceURL
	}
	baseURL := c.baseURL
	if baseURL == "" {
		baseURL = instanceURL
	}
	c.mu.Unlock()

	var endpoint string
	switch scope {
	case provider.ScopeRepo:
		if repo == "" {
			return "", fmt.Errorf("%w: repository name required for repo scope", ErrInvalidTargetURL)
		}
		endpoint = fmt.Sprintf("%s/api/v1/repos/%s/%s/actions/runners/registration-token", baseURL, owner, repo)
	case provider.ScopeOrg:
		if owner == "" {
			return "", fmt.Errorf("%w: organization name required for org scope", ErrInvalidTargetURL)
		}
		endpoint = fmt.Sprintf("%s/api/v1/orgs/%s/actions/runners/registration-token", baseURL, owner)
	case provider.ScopeGlobal:
		endpoint = fmt.Sprintf("%s/api/v1/admin/actions/runners/registration-token", baseURL)
	default:
		return "", fmt.Errorf("unsupported registration scope: %q", scope)
	}

	// Try GET first, fallback to POST on 405 Method Not Allowed
	tok, err := c.executeTokenRequest(ctx, http.MethodGet, endpoint)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.code == http.StatusMethodNotAllowed {
			return c.executeTokenRequest(ctx, http.MethodPost, endpoint)
		}
		return "", err
	}
	return tok, nil
}

// PollQueuedJobs queries Forgejo's API for queued Actions tasks (used for polling-based scaling).
func (c *Client) PollQueuedJobs(ctx context.Context, targetURL string) (int, error) {
	instanceURL, owner, repo, err := parseForgejoTargetURL(targetURL)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	if c.baseURL == "" && instanceURL != "" {
		c.baseURL = instanceURL
	}
	baseURL := c.baseURL
	if baseURL == "" {
		baseURL = instanceURL
	}
	c.mu.Unlock()

	var endpoint string
	if repo != "" {
		endpoint = fmt.Sprintf("%s/api/v1/repos/%s/%s/actions/tasks?status=waiting", baseURL, owner, repo)
	} else if owner != "" {
		endpoint = fmt.Sprintf("%s/api/v1/orgs/%s/actions/tasks?status=waiting", baseURL, owner)
	} else {
		endpoint = fmt.Sprintf("%s/api/v1/admin/actions/tasks?status=waiting", baseURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	c.setAuthHeader(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("polling Forgejo queued tasks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("forgejo polling API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading Forgejo polling response: %w", err)
	}

	// 1. Try parsing as array of task objects
	var taskList []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(bodyBytes, &taskList); err == nil {
		count := 0
		for _, task := range taskList {
			if task.Status == "waiting" || task.Status == "queued" || task.Status == "" {
				count++
			}
		}
		return count, nil
	}

	// 2. Try parsing as object with total_count or tasks list
	var taskObj struct {
		TotalCount int `json:"total_count"`
		Tasks      []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(bodyBytes, &taskObj); err == nil {
		if taskObj.TotalCount > 0 {
			return taskObj.TotalCount, nil
		}
		count := 0
		for _, task := range taskObj.Tasks {
			if task.Status == "waiting" || task.Status == "queued" || task.Status == "" {
				count++
			}
		}
		return count, nil
	}

	return 0, nil
}

// GetRenovateToken returns the Forgejo PAT for Renovate bot tasks.
func (c *Client) GetRenovateToken(ctx context.Context, targetURL string) (string, error) {
	return c.pat, nil
}

type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("forgejo API returned status %d: %s", e.code, e.msg)
}

func (c *Client) executeTokenRequest(ctx context.Context, method, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return "", err
	}
	c.setAuthHeader(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting Forgejo runner registration token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", &statusError{code: resp.StatusCode, msg: strings.TrimSpace(string(body))}
	}

	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("decoding Forgejo runner token response: %w", err)
	}
	if res.Token == "" {
		return "", ErrEmptyTokenReceived
	}
	return res.Token, nil
}

func (c *Client) setAuthHeader(req *http.Request) {
	if strings.HasPrefix(c.pat, "token ") || strings.HasPrefix(c.pat, "Bearer ") {
		req.Header.Set("Authorization", c.pat)
	} else {
		req.Header.Set("Authorization", "token "+c.pat)
	}
}

func parseForgejoTargetURL(rawURL string) (instanceURL, owner, repo string, err error) {
	clean := strings.TrimSpace(rawURL)
	clean = strings.TrimSuffix(clean, ".git")
	clean = strings.TrimSuffix(clean, "/")

	// Handle git@forgejo.example.com:owner/repo
	if strings.HasPrefix(clean, "git@") {
		parts := strings.Split(clean, ":")
		if len(parts) == 2 {
			host := strings.TrimPrefix(parts[0], "git@")
			instanceURL = "https://" + host
			subParts := strings.Split(parts[1], "/")
			if len(subParts) >= 2 {
				return instanceURL, subParts[0], subParts[1], nil
			}
			if len(subParts) == 1 {
				return instanceURL, subParts[0], "", nil
			}
		}
	}

	parsed, err := url.Parse(clean)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: %v", ErrInvalidTargetURL, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", "", ErrInvalidTargetURL
	}

	instanceURL = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	pathParts := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	if len(pathParts) > 0 && pathParts[0] != "" {
		owner = pathParts[0]
	}
	if len(pathParts) > 1 && pathParts[1] != "" {
		repo = pathParts[1]
	}
	return instanceURL, owner, repo, nil
}

// DiscoverOrganizations queries accessible organizations from the Forgejo instance.
func (c *Client) DiscoverOrganizations(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	c.mu.RUnlock()

	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("FORGEJO_INSTANCE_URL"), "/")
	}
	if baseURL == "" {
		return nil, errors.New("forgejo instance URL is required for target discovery (set FORGEJO_INSTANCE_URL)")
	}

	var targets []provider.DiscoveredTarget
	seen := make(map[string]bool)

	for page := 1; page <= 50; page++ {
		endpoint := fmt.Sprintf("%s/api/v1/user/orgs?limit=100&page=%d", baseURL, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		c.setAuthHeader(req)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling Forgejo API: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("listing Forgejo orgs failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var orgs []struct {
			UserName    string `json:"username"`
			FullName    string `json:"full_name"`
			AvatarURL   string `json:"avatar_url"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decoding Forgejo orgs response: %w", err)
		}
		_ = resp.Body.Close()

		for _, o := range orgs {
			name := o.UserName
			if seen[name] {
				continue
			}
			seen[name] = true

			fullName := o.FullName
			if fullName == "" {
				fullName = name
			}
			targets = append(targets, provider.DiscoveredTarget{
				Name:        name,
				FullName:    fullName,
				HTMLURL:     baseURL + "/" + name,
				Description: o.Description,
				AvatarURL:   o.AvatarURL,
			})
		}

		if len(orgs) < 100 {
			break
		}
	}

	return targets, nil
}

// DiscoverRepositories queries accessible repositories from the Forgejo instance.
func (c *Client) DiscoverRepositories(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	c.mu.RUnlock()

	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("FORGEJO_INSTANCE_URL"), "/")
	}
	if baseURL == "" {
		return nil, errors.New("forgejo instance URL is required for target discovery (set FORGEJO_INSTANCE_URL)")
	}

	var targets []provider.DiscoveredTarget
	seen := make(map[string]bool)

	for page := 1; page <= 50; page++ {
		endpoint := fmt.Sprintf("%s/api/v1/user/repos?limit=100&page=%d", baseURL, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		c.setAuthHeader(req)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling Forgejo API: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("listing Forgejo repos failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var repos []struct {
			Name        string `json:"name"`
			FullName    string `json:"full_name"`
			HTMLURL     string `json:"html_url"`
			Description string `json:"description"`
			Private     bool   `json:"private"`
			Owner       struct {
				AvatarURL string `json:"avatar_url"`
			} `json:"owner"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decoding Forgejo repos response: %w", err)
		}
		_ = resp.Body.Close()

		for _, r := range repos {
			if seen[r.HTMLURL] {
				continue
			}
			seen[r.HTMLURL] = true
			targets = append(targets, provider.DiscoveredTarget{
				Name:        r.Name,
				FullName:    r.FullName,
				HTMLURL:     r.HTMLURL,
				Description: r.Description,
				IsPrivate:   r.Private,
				AvatarURL:   r.Owner.AvatarURL,
			})
		}

		if len(repos) < 100 {
			break
		}
	}

	return targets, nil
}

func init() {
	provider.DefaultRegistry.Register(provider.AuthMethodForgejoToken, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		if profile.Token == "" {
			return nil, fmt.Errorf("%w: token is required for forgejo_token", provider.ErrMissingCredentials)
		}
		var opts []ClientOption
		token := profile.Token
		if strings.Contains(token, "|") {
			parts := strings.SplitN(token, "|", 2)
			if strings.HasPrefix(parts[0], "http") {
				opts = append(opts, WithBaseURL(parts[0]))
				token = parts[1]
			}
		}
		return NewClient(token, opts...)
	})
}
