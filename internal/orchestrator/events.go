package orchestrator

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

// ContainerEvent represents a container termination or destruction event.
type ContainerEvent struct {
	ContainerID string `json:"container_id"`
	PoolName    string `json:"pool_name"`
	// PoolID is the owning pool's database id from the container's pool-id
	// label (RUN-126); zero for containers spawned before the label existed,
	// in which case PoolName resolves the pool as a legacy fallback.
	PoolID    int64     `json:"pool_id,omitempty"`
	Action    string    `json:"action"` // "die", "destroy", "stop"
	ExitCode  int       `json:"exit_code"`
	Timestamp time.Time `json:"timestamp"`
}

// EventStreamProvider abstracts Docker event streaming for the orchestrator.
type EventStreamProvider interface {
	Events(ctx context.Context, options client.EventsListOptions) client.EventsResult
}

// ReapDeduplicator prevents duplicate runner provisioning races when both the real-time
// Docker event stream ("die", "destroy") and the periodic audit reconciler detect an exited
// container (docs/03 §3, §4).
type ReapDeduplicator struct {
	mu     sync.Mutex
	reaped map[string]time.Time
	ttl    time.Duration
}

// NewReapDeduplicator creates a new deduplicator with the given retention TTL.
func NewReapDeduplicator(ttl time.Duration) *ReapDeduplicator {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &ReapDeduplicator{
		reaped: make(map[string]time.Time),
		ttl:    ttl,
	}
}

// ShouldReap atomically checks whether containerID has already been reaped.
// Returns true on the first call for containerID; subsequent calls return false
// until the retention TTL expires.
func (d *ReapDeduplicator) ShouldReap(containerID string) bool {
	if containerID == "" {
		return false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	// Prune expired entries
	for id, t := range d.reaped {
		if now.Sub(t) > d.ttl {
			delete(d.reaped, id)
		}
	}

	if _, exists := d.reaped[containerID]; exists {
		return false
	}

	d.reaped[containerID] = now
	return true
}

// EventListener listens to Docker Engine events, triggering real-time container reaping
// for supervisor-managed containers (docs/03 §3).
type EventListener struct {
	provider     EventStreamProvider
	deduplicator *ReapDeduplicator
	backoff      time.Duration
	logCapturer  func(ctx context.Context, containerID string) error
}

// NewEventListener creates a new Docker event listener.
func NewEventListener(provider EventStreamProvider, deduplicator *ReapDeduplicator) *EventListener {
	if deduplicator == nil {
		deduplicator = NewReapDeduplicator(5 * time.Minute)
	}
	return &EventListener{
		provider:     provider,
		deduplicator: deduplicator,
		backoff:      500 * time.Millisecond,
	}
}

// SetLogCapturer registers a callback to capture container logs prior to reaping (OQ #14, #20).
func (l *EventListener) SetLogCapturer(capturer func(ctx context.Context, containerID string) error) {
	l.logCapturer = capturer
}

// Listen starts streaming container events, invoking onReap when a supervisor-managed
// container dies or is destroyed. Reconnects automatically with backoff upon stream error
// without leaking goroutines until ctx is canceled.
func (l *EventListener) Listen(ctx context.Context, onReap func(ContainerEvent)) error {
	filterArgs := make(client.Filters).
		Add("type", "container").
		Add("event", "die", "destroy").
		Add("label", LabelManaged+"=true")

	opts := client.EventsListOptions{
		Filters: filterArgs,
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		res := l.provider.Events(ctx, opts)
		msgCh, errCh := res.Messages, res.Err
		if msgCh == nil && errCh == nil {
			return nil
		}

		streamDone := false
		for !streamDone {
			select {
			case <-ctx.Done():
				return ctx.Err()

			case err, ok := <-errCh:
				if !ok || err != nil {
					streamDone = true
				}

			case msg, ok := <-msgCh:
				if !ok {
					streamDone = true
					break
				}

				action := string(msg.Action)
				if action != "die" && action != "destroy" {
					continue
				}

				containerID := msg.Actor.ID

				poolName := msg.Actor.Attributes[LabelPoolName]

				var poolID int64
				if rawPoolID := msg.Actor.Attributes[LabelPoolID]; rawPoolID != "" {
					if id, err := strconv.ParseInt(rawPoolID, 10, 64); err == nil {
						poolID = id
					}
				}

				exitCode := 0
				if exitCodeStr, ok := msg.Actor.Attributes["exitCode"]; ok {
					if ec, err := strconv.Atoi(exitCodeStr); err == nil {
						exitCode = ec
					}
				}

				event := ContainerEvent{
					ContainerID: containerID,
					PoolName:    poolName,
					PoolID:      poolID,
					Action:      action,
					ExitCode:    exitCode,
					Timestamp:   time.Unix(msg.Time, msg.TimeNano),
				}

				if l.deduplicator.ShouldReap(containerID) {
					if action == "die" && l.logCapturer != nil {
						_ = l.logCapturer(ctx, containerID)
					}
					if onReap != nil {
						onReap(event)
					}
				}
			}
		}

		// Stream disconnected, backoff before reconnecting
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.backoff):
		}
	}
}
