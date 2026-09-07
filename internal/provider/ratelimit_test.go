package provider_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/provider"
)

func TestRateLimitTransport_RetryAfterInteger(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt32(&attempts, 1)
		if att == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(func() { server.Close() })

	var sleptDelays []time.Duration
	transport := provider.NewRateLimitTransport("test-provider", server.Client().Transport)
	transport.SleepFn = func(ctx context.Context, d time.Duration) error {
		sleptDelays = append(sleptDelays, d)
		return nil
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if len(sleptDelays) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(sleptDelays))
	}
	if sleptDelays[0] != 5*time.Second {
		t.Errorf("expected 5s sleep, got %v", sleptDelays[0])
	}
}

func TestRateLimitTransport_RetryAfterDate(t *testing.T) {
	var attempts int32
	simulatedNow := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	retryDate := simulatedNow.Add(45 * time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt32(&attempts, 1)
		if att == 1 {
			w.Header().Set("Retry-After", retryDate.Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { server.Close() })

	var sleptDelays []time.Duration
	transport := provider.NewRateLimitTransport("test-provider", server.Client().Transport)
	transport.NowFn = func() time.Time { return simulatedNow }
	transport.SleepFn = func(ctx context.Context, d time.Duration) error {
		sleptDelays = append(sleptDelays, d)
		return nil
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if len(sleptDelays) != 1 || sleptDelays[0] != 45*time.Second {
		t.Errorf("expected 45s sleep, got %v", sleptDelays)
	}
}

func TestRateLimitTransport_GitHubResetHeader(t *testing.T) {
	var attempts int32
	simulatedNow := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	resetTime := simulatedNow.Add(120 * time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt32(&attempts, 1)
		if att == 1 {
			// GitHub returns 403 with X-RateLimit-Remaining: 0 and X-RateLimit-Reset
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { server.Close() })

	var sleptDelays []time.Duration
	transport := provider.NewRateLimitTransport("github", server.Client().Transport)
	transport.NowFn = func() time.Time { return simulatedNow }
	transport.SleepFn = func(ctx context.Context, d time.Duration) error {
		sleptDelays = append(sleptDelays, d)
		return nil
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if len(sleptDelays) != 1 || sleptDelays[0] != 120*time.Second {
		t.Errorf("expected 120s sleep, got %v", sleptDelays)
	}
}

func TestRateLimitTransport_ExponentialBackoffWithJitter(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt32(&attempts, 1)
		if att <= 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { server.Close() })

	var sleptDelays []time.Duration
	transport := provider.NewRateLimitTransport("gitea", server.Client().Transport)
	transport.BaseDelay = 100 * time.Millisecond
	transport.SleepFn = func(ctx context.Context, d time.Duration) error {
		sleptDelays = append(sleptDelays, d)
		return nil
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
	if len(sleptDelays) != 3 {
		t.Fatalf("expected 3 retries, got %d", len(sleptDelays))
	}

	// Attempt 0: ~100ms
	if sleptDelays[0] < 100*time.Millisecond || sleptDelays[0] > 130*time.Millisecond {
		t.Errorf("attempt 0 delay unexpected: %v", sleptDelays[0])
	}
	// Attempt 1: ~200ms
	if sleptDelays[1] < 200*time.Millisecond || sleptDelays[1] > 260*time.Millisecond {
		t.Errorf("attempt 1 delay unexpected: %v", sleptDelays[1])
	}
	// Attempt 2: ~400ms
	if sleptDelays[2] < 400*time.Millisecond || sleptDelays[2] > 520*time.Millisecond {
		t.Errorf("attempt 2 delay unexpected: %v", sleptDelays[2])
	}
}

func TestRateLimitTransport_MaxBackoffCap(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := atomic.AddInt32(&attempts, 1)
		if att == 1 {
			// Ask for 1 hour
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { server.Close() })

	var sleptDelay time.Duration
	transport := provider.NewRateLimitTransport("forgejo", server.Client().Transport)
	transport.SleepFn = func(ctx context.Context, d time.Duration) error {
		sleptDelay = d
		return nil
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Clamped to MaxBackoffCap (15 minutes)
	if sleptDelay != provider.MaxBackoffCap {
		t.Errorf("expected clamped to %v, got %v", provider.MaxBackoffCap, sleptDelay)
	}
}

func TestRateLimitTracker_PersistentAlert(t *testing.T) {
	var alertReceived provider.RateLimitAlert
	var wg sync.WaitGroup
	wg.Add(1)

	tracker := provider.NewRateLimitTracker(func(alert provider.RateLimitAlert) {
		alertReceived = alert
		wg.Done()
	})

	startTime := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	currentTime := startTime
	tracker.SetNowFn(func() time.Time { return currentTime })

	// Initial rate limit record: duration 0, no alert
	dur, raised := tracker.RecordRateLimit("github")
	if dur != 0 || raised {
		t.Errorf("initial record should not trigger alert: dur=%v raised=%v", dur, raised)
	}
	if !tracker.IsRateLimited("github") {
		t.Errorf("expected github to be tracked as rate-limited")
	}

	// 5 minutes later: duration 5m, still no alert
	currentTime = startTime.Add(5 * time.Minute)
	dur, raised = tracker.RecordRateLimit("github")
	if dur != 5*time.Minute || raised {
		t.Errorf("5m record should not trigger alert: dur=%v raised=%v", dur, raised)
	}

	// 11 minutes later: duration 11m, persistent alert triggered (>10m)
	currentTime = startTime.Add(11 * time.Minute)
	dur, raised = tracker.RecordRateLimit("github")
	if dur != 11*time.Minute || !raised {
		t.Errorf("11m record should trigger alert: dur=%v raised=%v", dur, raised)
	}

	wg.Wait()
	if alertReceived.Provider != "github" {
		t.Errorf("expected alert for github, got %q", alertReceived.Provider)
	}
	if alertReceived.Duration < 10*time.Minute {
		t.Errorf("expected alert duration >= 10m, got %v", alertReceived.Duration)
	}

	// 12 minutes later: already alerted, should not raise duplicate alert
	currentTime = startTime.Add(12 * time.Minute)
	_, raisedAgain := tracker.RecordRateLimit("github")
	if raisedAgain {
		t.Errorf("duplicate alert raised")
	}

	// Succeeded: clears state
	tracker.RecordSuccess("github")
	if tracker.IsRateLimited("github") {
		t.Errorf("expected github rate-limit state to be cleared")
	}
}

func TestRateLimitTransport_BodyReplay(t *testing.T) {
	var attempts int32
	var receivedBodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, string(body))

		att := atomic.AddInt32(&attempts, 1)
		if att == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { server.Close() })

	transport := provider.NewRateLimitTransport("test", server.Client().Transport)
	transport.SleepFn = func(ctx context.Context, d time.Duration) error { return nil }
	client := &http.Client{Transport: transport}

	payload := `{"action":"register"}`
	resp, err := client.Post(server.URL, "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if len(receivedBodies) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(receivedBodies))
	}
	if receivedBodies[0] != payload || receivedBodies[1] != payload {
		t.Errorf("body replay failed: %v", receivedBodies)
	}
}

func TestRateLimitTransport_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(func() { server.Close() })

	transport := provider.NewRateLimitTransport("test", server.Client().Transport)
	transport.SleepFn = func(ctx context.Context, d time.Duration) error {
		return context.Canceled
	}
	client := &http.Client{Transport: transport}

	_, err := client.Get(server.URL)
	if !errors.Is(err, provider.ErrContextCanceled) {
		t.Fatalf("expected ErrContextCanceled, got %v", err)
	}
}
