package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/noosxe/runnero/internal/db"
)

var (
	// ErrUnsupportedAuthMethod is returned when no constructor is registered for the requested auth method.
	ErrUnsupportedAuthMethod = errors.New("unsupported auth method")
	// ErrMissingCredentials is returned when required credentials for an auth method are missing.
	ErrMissingCredentials = errors.New("missing credentials for auth profile")
)

// ProviderConstructor builds a GitProvider from a decrypted auth profile.
type ProviderConstructor func(ctx context.Context, profile db.DecryptedAuthProfile) (GitProvider, error)

// Registry manages constructors for different auth methods and instantiates GitProviders.
type Registry struct {
	mu           sync.RWMutex
	constructors map[AuthMethod]ProviderConstructor
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		constructors: make(map[AuthMethod]ProviderConstructor),
	}
}

// Register registers a constructor for an auth method.
func (r *Registry) Register(method AuthMethod, constructor ProviderConstructor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.constructors[method] = constructor
}

// Build instantiates a GitProvider using the constructor registered for the profile's auth method.
func (r *Registry) Build(ctx context.Context, profile db.DecryptedAuthProfile) (GitProvider, error) {
	r.mu.RLock()
	constructor, exists := r.constructors[AuthMethod(profile.AuthMethod)]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAuthMethod, profile.AuthMethod)
	}

	return constructor(ctx, profile)
}

// BuildFromDB loads a decrypted auth profile by ID from the database and constructs the GitProvider.
func (r *Registry) BuildFromDB(ctx context.Context, database *db.DB, authProfileID int64) (GitProvider, error) {
	profile, err := database.GetDecryptedAuthProfileById(ctx, authProfileID)
	if err != nil {
		return nil, fmt.Errorf("loading auth profile %d: %w", authProfileID, err)
	}
	return r.Build(ctx, *profile)
}

// DefaultRegistry is the global provider registry.
var DefaultRegistry = NewRegistry()
