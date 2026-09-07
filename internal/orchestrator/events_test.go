package orchestrator_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"github.com/noosxe/runnero/internal/orchestrator"
)

type mockEventStreamProvider struct {
	msgCh chan events.Message
	errCh chan error
}

func (m *mockEventStreamProvider) Events(ctx context.Context, options client.EventsListOptions) client.EventsResult {
	return client.EventsResult{
		Messages: m.msgCh,
		Err:      m.errCh,
	}
}

func TestReapDeduplicator(t *testing.T) {
	dedup := orchestrator.NewReapDeduplicator(100 * time.Millisecond)

	// 1. First reap succeeds
	if !dedup.ShouldReap("container-1") {
		t.Fatalf("expected true on first reap call")
	}

	// 2. Duplicate reap for same container fails (preventing duplicate spawns)
	if dedup.ShouldReap("container-1") {
		t.Fatalf("expected false on duplicate reap call")
	}

	// 3. Different container succeeds
	if !dedup.ShouldReap("container-2") {
		t.Fatalf("expected true for container-2")
	}

	// 4. After TTL expires, entry is pruned and can be reaped again
	time.Sleep(150 * time.Millisecond)
	if !dedup.ShouldReap("container-1") {
		t.Fatalf("expected true after TTL expired")
	}
}

func TestEventListener_RealTimeReapAndDeduplication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgCh := make(chan events.Message, 10)
	errCh := make(chan error, 10)

	provider := &mockEventStreamProvider{
		msgCh: msgCh,
		errCh: errCh,
	}

	dedup := orchestrator.NewReapDeduplicator(5 * time.Minute)
	listener := orchestrator.NewEventListener(provider, dedup)

	reapedCount := int32(0)
	reapedChan := make(chan orchestrator.ContainerEvent, 5)

	go func() {
		_ = listener.Listen(ctx, func(e orchestrator.ContainerEvent) {
			atomic.AddInt32(&reapedCount, 1)
			reapedChan <- e
		})
	}()

	// 1. Emit "die" event for a managed runner container
	dieMsg := events.Message{
		Action: "die",
		Actor: events.Actor{
			ID: "c-runner-1",
			Attributes: map[string]string{
				orchestrator.LabelPoolName: "arm64-pool",
				"exitCode":                 "0",
			},
		},
		Time:     time.Now().Unix(),
		TimeNano: 0,
	}

	start := time.Now()
	msgCh <- dieMsg

	select {
	case event := <-reapedChan:
		latency := time.Since(start)
		// Acceptance criterion: <2s in test env
		if latency > 2*time.Second {
			t.Errorf("reap event latency %v exceeded 2s limit", latency)
		}
		if event.ContainerID != "c-runner-1" || event.PoolName != "arm64-pool" || event.Action != "die" {
			t.Errorf("unexpected event payload: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reap event")
	}

	// 2. Emit duplicate "destroy" event for the exact same container
	destroyMsg := events.Message{
		Action: "destroy",
		Actor: events.Actor{
			ID: "c-runner-1",
			Attributes: map[string]string{
				orchestrator.LabelPoolName: "arm64-pool",
			},
		},
	}
	msgCh <- destroyMsg

	// Give slight pause to ensure no double-reap occurs
	time.Sleep(50 * time.Millisecond)
	if count := atomic.LoadInt32(&reapedCount); count != 1 {
		t.Fatalf("expected deduplication to prevent second reap: count=%d", count)
	}

	// 3. Emit stream error to verify reconnect without goroutine leaks
	errCh <- errors.New("stream closed")
	time.Sleep(20 * time.Millisecond)

	// Emit event for new container to verify listener reconnected
	newMsg := events.Message{
		Action: "die",
		Actor: events.Actor{
			ID: "c-runner-2",
			Attributes: map[string]string{
				orchestrator.LabelPoolName: "arm64-pool",
			},
		},
	}
	msgCh <- newMsg

	select {
	case event := <-reapedChan:
		if event.ContainerID != "c-runner-2" {
			t.Errorf("unexpected event container ID: %s", event.ContainerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reaped event after reconnect")
	}
}
