package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/provider"
)

const (
	// DefaultBaseURL is the standard public GitHub API root.
	DefaultBaseURL = "https://api.github.com"
	// GitHubAPIVersion is the recommended GitHub API version header.
	GitHubAPIVersion = "2022-11-28"
)

var (
	// ErrInvalidTargetURL is returned when target URL cannot be parsed into owner/repo.
	ErrInvalidTargetURL = errors.New("invalid GitHub target URL: must specify owner or owner/repo")
	// ErrInstallationNotFound is returned when no GitHub App installation is found for the target.
	ErrInstallationNotFound = errors.New("github app installation not found for target")
)

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// Client implements provider.GitProvider and provider.RenovateTokenProvider for GitHub.
type Client struct {
	baseURL    string
	authMethod provider.AuthMethod
	appID      int64
	privateKey *rsa.PrivateKey
	pat        string
	httpClient *http.Client

	mu         sync.Mutex
	tokenCache map[string]cachedToken
	appSlug    string
	appHTMLURL string
}

// ClientOption configures a GitHub Client.
type ClientOption func(*Client)

// WithBaseURL overrides the GitHub API base URL (useful for testing and GHE).
func WithBaseURL(rawURL string) ClientOption {
	return func(c *Client) {
		if rawURL != "" {
			c.baseURL = strings.TrimRight(rawURL, "/")
		}
	}
}

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// NewAppProvider creates a GitHub provider using GitHub App authentication.
func NewAppProvider(appID int64, pemPrivateKey string, opts ...ClientOption) (*Client, error) {
	key, err := ParseRSAPrivateKey([]byte(pemPrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parsing GitHub App private key: %w", err)
	}

	c := &Client{
		baseURL:    DefaultBaseURL,
		authMethod: provider.AuthMethodGitHubApp,
		appID:      appID,
		privateKey: key,
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: provider.NewRateLimitTransport("github", http.DefaultTransport)},
		tokenCache: make(map[string]cachedToken),
	}

	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// NewPATProvider creates a GitHub provider using Personal Access Token authentication.
func NewPATProvider(pat string, opts ...ClientOption) (*Client, error) {
	if strings.TrimSpace(pat) == "" {
		return nil, fmt.Errorf("%w: PAT cannot be empty", provider.ErrMissingCredentials)
	}

	c := &Client{
		baseURL:    DefaultBaseURL,
		authMethod: provider.AuthMethodPAT,
		pat:        pat,
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: provider.NewRateLimitTransport("github", http.DefaultTransport)},
		tokenCache: make(map[string]cachedToken),
	}

	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// ScalingMode returns ScalingWebhook because GitHub uses workflow_job webhooks.
func (c *Client) ScalingMode() provider.ScalingMode {
	return provider.ScalingWebhook
}

// PollQueuedJobs is a no-op for GitHub since scaling is webhook-driven.
func (c *Client) PollQueuedJobs(ctx context.Context, targetURL string) (int, error) {
	return 0, nil
}

// ValidateCredentials verifies the credentials against the GitHub API.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	var req *http.Request
	var err error

	if c.authMethod == provider.AuthMethodGitHubApp {
		jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
		if err != nil {
			return fmt.Errorf("generating app JWT: %w", err)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/app", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/user", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.pat)
	}

	c.setCommonHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling GitHub API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub credentials validation failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// GetRegistrationToken retrieves a short-lived runner registration token.
func (c *Client) GetRegistrationToken(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
	owner, repo, err := parseTargetURL(targetURL)
	if err != nil {
		return "", err
	}

	authToken, err := c.getAuthBearerToken(ctx, owner, repo)
	if err != nil {
		return "", err
	}

	var endpoint string
	switch scope {
	case provider.ScopeRepo:
		if repo == "" {
			return "", fmt.Errorf("%w: repository name required for repo scope", ErrInvalidTargetURL)
		}
		endpoint = fmt.Sprintf("%s/repos/%s/%s/actions/runners/registration-token", c.baseURL, owner, repo)
	case provider.ScopeOrg:
		endpoint = fmt.Sprintf("%s/orgs/%s/actions/runners/registration-token", c.baseURL, owner)
	case provider.ScopeGlobal:
		endpoint = fmt.Sprintf("%s/enterprises/%s/actions/runners/registration-token", c.baseURL, owner)
	default:
		return "", fmt.Errorf("unsupported registration scope: %q", scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	c.setCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting runner registration token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get runner registration token (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decoding registration token response: %w", err)
	}
	if tokenResp.Token == "" {
		return "", errors.New("empty registration token received from GitHub API")
	}
	return tokenResp.Token, nil
}

// GetRenovateToken returns an installation access token (for Apps) or the PAT directly.
func (c *Client) GetRenovateToken(ctx context.Context, targetURL string) (string, error) {
	if c.authMethod == provider.AuthMethodPAT {
		return c.pat, nil
	}

	owner, repo, err := parseTargetURL(targetURL)
	if err != nil {
		return "", err
	}
	return c.getInstallationToken(ctx, owner, repo)
}

func (c *Client) getAuthBearerToken(ctx context.Context, owner, repo string) (string, error) {
	if c.authMethod == provider.AuthMethodPAT {
		return c.pat, nil
	}
	return c.getInstallationToken(ctx, owner, repo)
}

func (c *Client) getInstallationToken(ctx context.Context, owner, repo string) (string, error) {
	cacheKey := owner + "/" + repo

	c.mu.Lock()
	if cached, ok := c.tokenCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		c.mu.Unlock()
		return cached.token, nil
	}
	c.mu.Unlock()

	installationID, err := c.findInstallationID(ctx, owner, repo)
	if err != nil {
		return "", err
	}

	jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	c.setCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting installation access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to create installation token (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("decoding installation token response: %w", err)
	}

	c.mu.Lock()
	// Cache until 2 minutes before actual expiry
	c.tokenCache[cacheKey] = cachedToken{
		token:     res.Token,
		expiresAt: res.ExpiresAt.Add(-2 * time.Minute),
	}
	c.mu.Unlock()

	return res.Token, nil
}

func (c *Client) getInstallationTokenByID(ctx context.Context, installationID int64) (string, error) {
	jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	c.setCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting installation access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to create installation token (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.Token, nil
}

var _ provider.AppMetadataProvider = (*Client)(nil)

// GetAppMetadata returns the app's install URL and active installations list.
func (c *Client) GetAppMetadata(ctx context.Context) (string, []provider.AppInstallation, error) {
	if c.authMethod != provider.AuthMethodGitHubApp {
		return "", nil, nil
	}

	c.mu.Lock()
	slug := c.appSlug
	c.mu.Unlock()

	jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
	if err != nil {
		return "", nil, fmt.Errorf("generating app JWT: %w", err)
	}

	if slug == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/app", nil)
		if err != nil {
			return "", nil, err
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		c.setCommonHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", nil, fmt.Errorf("fetching app metadata: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusOK {
			var appData struct {
				Slug    string `json:"slug"`
				HTMLURL string `json:"html_url"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&appData); err == nil {
				slug = appData.Slug
				c.mu.Lock()
				c.appSlug = appData.Slug
				c.appHTMLURL = appData.HTMLURL
				c.mu.Unlock()
			}
		}
	}

	installURL := ""
	if slug != "" {
		webBase := "https://github.com"
		if !strings.Contains(c.baseURL, "api.github.com") {
			webBase = strings.TrimSuffix(c.baseURL, "/api/v3")
		}
		installURL = fmt.Sprintf("%s/apps/%s/installations/new", webBase, slug)
	}

	var rawInstallations []struct {
		ID                  int64  `json:"id"`
		HTMLURL             string `json:"html_url"`
		RepositorySelection string `json:"repository_selection"`
		Account             struct {
			Login   string `json:"login"`
			Type    string `json:"type"`
			HTMLURL string `json:"html_url"`
		} `json:"account"`
	}

	for page := 1; page <= 50; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/app/installations?per_page=100&page=%d", c.baseURL, page), nil)
		if err != nil {
			return installURL, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		c.setCommonHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return installURL, nil, fmt.Errorf("listing app installations: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return installURL, nil, fmt.Errorf("listing app installations (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var pageInsts []struct {
			ID                  int64  `json:"id"`
			HTMLURL             string `json:"html_url"`
			RepositorySelection string `json:"repository_selection"`
			Account             struct {
				Login   string `json:"login"`
				Type    string `json:"type"`
				HTMLURL string `json:"html_url"`
			} `json:"account"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&pageInsts)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return installURL, nil, fmt.Errorf("decoding installations: %w", decodeErr)
		}

		rawInstallations = append(rawInstallations, pageInsts...)
		if len(pageInsts) < 100 {
			break
		}
	}

	installations := make([]provider.AppInstallation, 0, len(rawInstallations))
	for _, inst := range rawInstallations {
		htmlURL := inst.HTMLURL
		if htmlURL == "" {
			if inst.Account.Type == "Organization" {
				htmlURL = fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", inst.Account.Login, inst.ID)
			} else {
				htmlURL = fmt.Sprintf("https://github.com/settings/installations/%d", inst.ID)
			}
		}
		installations = append(installations, provider.AppInstallation{
			ID:                  inst.ID,
			AccountLogin:        inst.Account.Login,
			AccountType:         inst.Account.Type,
			HTMLURL:             htmlURL,
			RepositorySelection: inst.RepositorySelection,
		})
	}

	return installURL, installations, nil
}

// DiscoverOrganizations queries organizations accessible to the configured credentials.
func (c *Client) DiscoverOrganizations(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	if c.authMethod == provider.AuthMethodGitHubApp {
		jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
		if err != nil {
			return nil, fmt.Errorf("generating app JWT: %w", err)
		}

		var targets []provider.DiscoveredTarget
		seen := make(map[string]bool)

		for page := 1; page <= 50; page++ {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/app/installations?per_page=100&page=%d", c.baseURL, page), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+jwt)
			c.setCommonHeaders(req)

			resp, err := c.httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("listing app installations: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return nil, fmt.Errorf("listing app installations (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}

			var installations []struct {
				ID      int64 `json:"id"`
				Account struct {
					Login       string `json:"login"`
					HTMLURL     string `json:"html_url"`
					AvatarURL   string `json:"avatar_url"`
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"account"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&installations); err != nil {
				_ = resp.Body.Close()
				return nil, fmt.Errorf("decoding installations: %w", err)
			}
			_ = resp.Body.Close()

			for _, inst := range installations {
				if seen[inst.Account.Login] {
					continue
				}
				seen[inst.Account.Login] = true

				htmlURL := inst.Account.HTMLURL
				if htmlURL == "" {
					htmlURL = "https://github.com/" + inst.Account.Login
				}
				targets = append(targets, provider.DiscoveredTarget{
					Name:        inst.Account.Login,
					FullName:    inst.Account.Login,
					HTMLURL:     htmlURL,
					Description: inst.Account.Description,
					AvatarURL:   inst.Account.AvatarURL,
				})
			}

			if len(installations) < 100 {
				break
			}
		}

		return targets, nil
	}

	// PAT mode
	var targets []provider.DiscoveredTarget
	seen := make(map[string]bool)

	for page := 1; page <= 50; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/user/orgs?per_page=100&page=%d", c.baseURL, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.pat)
		c.setCommonHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing user orgs: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("listing user orgs (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var orgs []struct {
			Login       string `json:"login"`
			Description string `json:"description"`
			AvatarURL   string `json:"avatar_url"`
			HTMLURL     string `json:"html_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decoding user orgs: %w", err)
		}
		_ = resp.Body.Close()

		for _, o := range orgs {
			if seen[o.Login] {
				continue
			}
			seen[o.Login] = true

			htmlURL := o.HTMLURL
			if htmlURL == "" {
				htmlURL = "https://github.com/" + o.Login
			}
			targets = append(targets, provider.DiscoveredTarget{
				Name:        o.Login,
				FullName:    o.Login,
				HTMLURL:     htmlURL,
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

// DiscoverRepositories queries repositories accessible to the configured credentials.
func (c *Client) DiscoverRepositories(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	if c.authMethod == provider.AuthMethodGitHubApp {
		jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
		if err != nil {
			return nil, fmt.Errorf("generating app JWT: %w", err)
		}

		var installations []struct {
			ID int64 `json:"id"`
		}
		for page := 1; page <= 50; page++ {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/app/installations?per_page=100&page=%d", c.baseURL, page), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+jwt)
			c.setCommonHeaders(req)

			resp, err := c.httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("listing app installations: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return nil, fmt.Errorf("listing app installations (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}

			var pageInsts []struct {
				ID int64 `json:"id"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&pageInsts); err != nil {
				_ = resp.Body.Close()
				return nil, fmt.Errorf("decoding installations: %w", err)
			}
			_ = resp.Body.Close()

			installations = append(installations, pageInsts...)
			if len(pageInsts) < 100 {
				break
			}
		}

		var targets []provider.DiscoveredTarget
		seen := make(map[string]bool)
		for _, inst := range installations {
			tok, err := c.getInstallationTokenByID(ctx, inst.ID)
			if err != nil {
				continue
			}

			for repoPage := 1; repoPage <= 50; repoPage++ {
				reposReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", c.baseURL, repoPage), nil)
				if err != nil {
					break
				}
				reposReq.Header.Set("Authorization", "Bearer "+tok)
				c.setCommonHeaders(reposReq)

				reposResp, err := c.httpClient.Do(reposReq)
				if err != nil {
					break
				}

				if reposResp.StatusCode != http.StatusOK {
					_ = reposResp.Body.Close()
					break
				}

				var reposData struct {
					Repositories []struct {
						Name        string `json:"name"`
						FullName    string `json:"full_name"`
						HTMLURL     string `json:"html_url"`
						Description string `json:"description"`
						Private     bool   `json:"private"`
						Owner       struct {
							AvatarURL string `json:"avatar_url"`
						} `json:"owner"`
					} `json:"repositories"`
				}
				_ = json.NewDecoder(reposResp.Body).Decode(&reposData)
				_ = reposResp.Body.Close()

				for _, r := range reposData.Repositories {
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

				if len(reposData.Repositories) < 100 {
					break
				}
			}
		}
		return targets, nil
	}

	// PAT mode
	var targets []provider.DiscoveredTarget
	seen := make(map[string]bool)

	for page := 1; page <= 50; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/user/repos?per_page=100&sort=updated&page=%d", c.baseURL, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.pat)
		c.setCommonHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing user repos: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("listing user repos (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
			return nil, fmt.Errorf("decoding user repos: %w", err)
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

func (c *Client) findInstallationID(ctx context.Context, owner, repo string) (int64, error) {
	jwt, err := GenerateAppJWT(c.appID, c.privateKey, time.Now())
	if err != nil {
		return 0, err
	}

	// 1. If repo is specified, query /repos/{owner}/{repo}/installation
	if repo != "" {
		id, err := c.queryInstallationEndpoint(ctx, jwt, fmt.Sprintf("%s/repos/%s/%s/installation", c.baseURL, owner, repo))
		if err == nil {
			return id, nil
		}
	}

	// 2. Query /orgs/{owner}/installation
	id, err := c.queryInstallationEndpoint(ctx, jwt, fmt.Sprintf("%s/orgs/%s/installation", c.baseURL, owner))
	if err == nil {
		return id, nil
	}

	// 3. Query /users/{owner}/installation
	id, err = c.queryInstallationEndpoint(ctx, jwt, fmt.Sprintf("%s/users/%s/installation", c.baseURL, owner))
	if err == nil {
		return id, nil
	}

	return 0, fmt.Errorf("%w for target %s/%s", ErrInstallationNotFound, owner, repo)
}

func (c *Client) queryInstallationEndpoint(ctx context.Context, jwt, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	c.setCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}
	if res.ID <= 0 {
		return 0, errors.New("empty or invalid installation id")
	}
	return res.ID, nil
}

func (c *Client) setCommonHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", GitHubAPIVersion)
	req.Header.Set("User-Agent", "runnero-supervisor")
}

func parseTargetURL(rawURL string) (owner, repo string, err error) {
	clean := strings.TrimSpace(rawURL)
	clean = strings.TrimSuffix(clean, ".git")
	clean = strings.TrimSuffix(clean, "/")

	// Handle git@github.com:owner/repo
	if strings.HasPrefix(clean, "git@") {
		parts := strings.Split(clean, ":")
		if len(parts) == 2 {
			subParts := strings.Split(parts[1], "/")
			if len(subParts) >= 2 {
				return subParts[0], subParts[1], nil
			}
			if len(subParts) == 1 {
				return subParts[0], "", nil
			}
		}
	}

	parsed, err := url.Parse(clean)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidTargetURL, err)
	}

	pathParts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		return "", "", ErrInvalidTargetURL
	}

	owner = pathParts[0]
	if len(pathParts) > 1 {
		repo = pathParts[1]
	}
	return owner, repo, nil
}

func init() {
	provider.DefaultRegistry.Register(provider.AuthMethodGitHubApp, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		if !profile.AppID.Valid || profile.AppID.Int64 <= 0 {
			return nil, fmt.Errorf("%w: app_id is required for github_app", provider.ErrMissingCredentials)
		}
		if profile.PrivateKey == "" {
			return nil, fmt.Errorf("%w: private_key is required for github_app", provider.ErrMissingCredentials)
		}
		return NewAppProvider(profile.AppID.Int64, profile.PrivateKey)
	})

	provider.DefaultRegistry.Register(provider.AuthMethodPAT, func(ctx context.Context, profile db.DecryptedAuthProfile) (provider.GitProvider, error) {
		if profile.Token == "" {
			return nil, fmt.Errorf("%w: token is required for pat auth", provider.ErrMissingCredentials)
		}
		return NewPATProvider(profile.Token)
	})
}
