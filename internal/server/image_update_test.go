package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

type mockImageUpdateDB struct {
	mu    sync.Mutex
	pools map[int64]db.RunnerPool
	logs  []db.AuditLog
}

func (m *mockImageUpdateDB) ListRunnerPools(_ context.Context) ([]db.RunnerPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []db.RunnerPool
	for _, p := range m.pools {
		res = append(res, p)
	}
	return res, nil
}

func (m *mockImageUpdateDB) GetRunnerPoolById(_ context.Context, id int64) (db.RunnerPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pools[id]
	if !ok {
		return db.RunnerPool{}, fmt.Errorf("not found")
	}
	return p, nil
}

func (m *mockImageUpdateDB) CreateAuditLog(_ context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := db.AuditLog{
		ID:        int64(len(m.logs) + 1),
		Action:    arg.Action,
		CreatedAt: time.Now().UTC(),
	}
	m.logs = append(m.logs, l)
	return l, nil
}

type mockImagePuller struct {
	mu           sync.Mutex
	pulledImages []string
	pullFn       func(ctx context.Context, image string) error
}

func (m *mockImagePuller) PullImage(ctx context.Context, image string) error {
	m.mu.Lock()
	m.pulledImages = append(m.pulledImages, image)
	fn := m.pullFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, image)
	}
	return nil
}

func (m *mockImagePuller) Pulled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.pulledImages))
	copy(cp, m.pulledImages)
	return cp
}

func TestImageUpdateServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			1: {
				ID:          1,
				Name:        "pool-linux-ci",
				RunnerImage: "ghcr.io/noosxe/runnero:v1.1.0",
			},
		},
	}
	mockPuller := &mockImagePuller{}

	svc := NewImageUpdateService(mockDB, mockPuller, WithSyncPull(true))

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	// 1. Check Image Update
	checkRes, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{
		PoolId: 1,
	}))
	if err != nil {
		t.Fatalf("CheckImageUpdate failed: %v", err)
	}
	if !checkRes.Msg.UpdateAvailable {
		t.Fatalf("expected update to be available")
	}
	if checkRes.Msg.Update.PoolId != 1 {
		t.Errorf("expected pool_id=1, got %d", checkRes.Msg.Update.PoolId)
	}

	// 2. List Image Updates
	listRes, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{
		PoolId: 1,
	}))
	if err != nil {
		t.Fatalf("ListImageUpdates failed: %v", err)
	}
	if len(listRes.Msg.Updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(listRes.Msg.Updates))
	}
	if listRes.Msg.Updates[0].Id <= 0 {
		t.Errorf("expected positive update ID")
	}

	// 3. Pull Image
	pullRes, err := client.PullImage(ctx, connect.NewRequest(&supervisorv1.PullImageRequest{
		PoolId: 1,
	}))
	if err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}
	if !pullRes.Msg.Success {
		t.Errorf("PullImage response success = false")
	}
	pulled := mockPuller.Pulled()
	if len(pulled) != 1 || pulled[0] != "ghcr.io/noosxe/runnero:v1.1.0" {
		t.Errorf("expected pulled image %s, got %v", "ghcr.io/noosxe/runnero:v1.1.0", pulled)
	}

	// 4. Dismiss Image Update
	newUp := svc.FlagUpdate(1, "ghcr.io/noosxe/runnero:v1.1.0", "ghcr.io/noosxe/runnero:v1.2.0")
	dismissRes, err := client.DismissImageUpdate(ctx, connect.NewRequest(&supervisorv1.DismissImageUpdateRequest{
		Id: newUp.Id,
	}))
	if err != nil {
		t.Fatalf("DismissImageUpdate failed: %v", err)
	}
	if !dismissRes.Msg.Success {
		t.Errorf("DismissImageUpdate success = false")
	}
}

type mockLocalInspector struct {
	mu      sync.RWMutex
	digests map[string]string
}

func (m *mockLocalInspector) GetLocalImageDigest(_ context.Context, image string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if d, ok := m.digests[image]; ok {
		return d, nil
	}
	return "", fmt.Errorf("local image not found")
}

func (m *mockLocalInspector) SetDigest(image, digest string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.digests[image] = digest
}

type mockRegistryChecker struct {
	mu      sync.RWMutex
	digests map[string]string
}

func (m *mockRegistryChecker) GetRemoteImageDigest(_ context.Context, imageRef string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if d, ok := m.digests[imageRef]; ok {
		return d, nil
	}
	return "", fmt.Errorf("remote image not found")
}

func (m *mockRegistryChecker) BumpTag(imageRef, newDigest string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.digests[imageRef] = newDigest
}

func TestCheckImageUpdate_DetectsBumpedTag(t *testing.T) {
	ctx := context.Background()
	testImage := "ghcr.io/noosxe/runnero:v1.0.0"
	initialDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	bumpedDigest := "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			10: {
				ID:          10,
				Name:        "pool-test",
				RunnerImage: testImage,
			},
		},
	}
	mockPuller := &mockImagePuller{}
	inspector := &mockLocalInspector{
		digests: map[string]string{
			testImage: initialDigest,
		},
	}
	registry := &mockRegistryChecker{
		digests: map[string]string{
			testImage: initialDigest,
		},
	}

	svc := NewImageUpdateService(mockDB, mockPuller,
		WithLocalInspector(inspector),
		WithRegistryChecker(registry),
		WithSyncPull(true),
	)

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	// Phase 1: Local digest matches registry digest -> No update available
	res1, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{
		PoolId: 10,
	}))
	if err != nil {
		t.Fatalf("CheckImageUpdate failed: %v", err)
	}
	if res1.Msg.UpdateAvailable {
		t.Fatalf("expected update_available=false when digests match")
	}
	if res1.Msg.Update != nil {
		t.Errorf("expected update to be nil, got %+v", res1.Msg.Update)
	}

	// Phase 2: Intentionally bump test tag on registry
	registry.BumpTag(testImage, bumpedDigest)

	// Phase 3: CheckImageUpdate must detect the bumped tag (Acceptance requirement)
	res2, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{
		PoolId: 10,
	}))
	if err != nil {
		t.Fatalf("CheckImageUpdate failed: %v", err)
	}
	if !res2.Msg.UpdateAvailable {
		t.Fatalf("expected update_available=true after bumping test tag")
	}
	if res2.Msg.Update == nil {
		t.Fatalf("expected update details to be returned")
	}
	if res2.Msg.Update.PoolId != 10 {
		t.Errorf("expected pool_id=10, got %d", res2.Msg.Update.PoolId)
	}
	if res2.Msg.Update.LatestDigest != bumpedDigest {
		t.Errorf("expected latest_digest=%s, got %s", bumpedDigest, res2.Msg.Update.LatestDigest)
	}
	if res2.Msg.Update.CurrentImage != testImage {
		t.Errorf("expected current_image=%s, got %s", testImage, res2.Msg.Update.CurrentImage)
	}
	if res2.Msg.Update.Status != "available" {
		t.Errorf("expected status=available, got %s", res2.Msg.Update.Status)
	}

	// Verify update surfaces on dashboard via ListImageUpdates
	listRes, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{
		PoolId: 10,
	}))
	if err != nil {
		t.Fatalf("ListImageUpdates failed: %v", err)
	}
	if len(listRes.Msg.Updates) != 1 {
		t.Fatalf("expected 1 update notification, got %d", len(listRes.Msg.Updates))
	}
	if listRes.Msg.Updates[0].LatestDigest != bumpedDigest {
		t.Errorf("expected latest digest %s in list, got %s", bumpedDigest, listRes.Msg.Updates[0].LatestDigest)
	}

	// Phase 4: Pull image and update local inspector -> subsequent check resolves update
	pullRes, err := client.PullImage(ctx, connect.NewRequest(&supervisorv1.PullImageRequest{
		PoolId: 10,
	}))
	if err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}
	if !pullRes.Msg.Success {
		t.Fatalf("expected PullImage success")
	}

	inspector.SetDigest(testImage, bumpedDigest)

	res3, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{
		PoolId: 10,
	}))
	if err != nil {
		t.Fatalf("CheckImageUpdate failed: %v", err)
	}
	if res3.Msg.UpdateAvailable {
		t.Fatalf("expected update_available=false after pulling updated image")
	}

	// Notification should be cleared
	listRes2, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{
		PoolId: 10,
	}))
	if err != nil {
		t.Fatalf("ListImageUpdates failed: %v", err)
	}
	if len(listRes2.Msg.Updates) != 0 {
		t.Errorf("expected 0 pending updates after resolution, got %d", len(listRes2.Msg.Updates))
	}
}

func TestCheckImageUpdate_PerPoolScoping(t *testing.T) {
	ctx := context.Background()

	pool1Image := "ghcr.io/noosxe/runnero:v1.0.0"
	pool2Image := "ghcr.io/noosxe/runnero:v2.0.0"

	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			1: {ID: 1, Name: "prod-pool", RunnerImage: pool1Image},
			2: {ID: 2, Name: "staging-pool", RunnerImage: pool2Image},
		},
	}
	mockPuller := &mockImagePuller{}

	inspector := &mockLocalInspector{
		digests: map[string]string{
			pool1Image: "sha256:v1-local",
			pool2Image: "sha256:v2-local",
		},
	}
	registry := &mockRegistryChecker{
		digests: map[string]string{
			pool1Image: "sha256:v1-local",
			pool2Image: "sha256:v2-new-bumped", // Only pool 2 has an update
		},
	}

	svc := NewImageUpdateService(mockDB, mockPuller,
		WithLocalInspector(inspector),
		WithRegistryChecker(registry),
	)

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	// Check pool 1 (prod) -> no update
	p1Res, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{PoolId: 1}))
	if err != nil {
		t.Fatalf("check pool 1 failed: %v", err)
	}
	if p1Res.Msg.UpdateAvailable {
		t.Errorf("pool 1 should not have an update available")
	}

	// Check pool 2 (staging) -> update available!
	p2Res, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{PoolId: 2}))
	if err != nil {
		t.Fatalf("check pool 2 failed: %v", err)
	}
	if !p2Res.Msg.UpdateAvailable {
		t.Errorf("pool 2 should have an update available")
	}
	if p2Res.Msg.Update.LatestDigest != "sha256:v2-new-bumped" {
		t.Errorf("expected latest digest sha256:v2-new-bumped, got %s", p2Res.Msg.Update.LatestDigest)
	}

	// Verify scoping in ListImageUpdates
	listP1, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{PoolId: 1}))
	if err != nil {
		t.Fatalf("list pool 1 failed: %v", err)
	}
	if len(listP1.Msg.Updates) != 0 {
		t.Errorf("expected 0 updates for pool 1, got %d", len(listP1.Msg.Updates))
	}

	listP2, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{PoolId: 2}))
	if err != nil {
		t.Fatalf("list pool 2 failed: %v", err)
	}
	if len(listP2.Msg.Updates) != 1 {
		t.Errorf("expected 1 update for pool 2, got %d", len(listP2.Msg.Updates))
	}
}

func TestCheckImageUpdate_RegistryError(t *testing.T) {
	ctx := context.Background()

	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			1: {ID: 1, Name: "pool-err", RunnerImage: "ghcr.io/noosxe/unknown:latest"},
		},
	}
	mockPuller := &mockImagePuller{}

	registry := &mockRegistryChecker{
		digests: map[string]string{}, // empty -> will return error
	}

	svc := NewImageUpdateService(mockDB, mockPuller, WithRegistryChecker(registry))

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	_, err := client.CheckImageUpdate(ctx, connect.NewRequest(&supervisorv1.CheckImageUpdateRequest{PoolId: 1}))
	if err == nil {
		t.Fatalf("expected error when registry check fails, got nil")
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("expected CodeUnavailable, got %v", connect.CodeOf(err))
	}
}

func TestImageUpdate_BackgroundPullAndStatusTransition(t *testing.T) {
	ctx := context.Background()
	testImage := "ghcr.io/noosxe/runnero:v1.1.0"
	bumpedDigest := "sha256:3333333333333333333333333333333333333333333333333333333333333333"

	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			42: {ID: 42, Name: "pool-async", RunnerImage: testImage},
		},
	}

	pullStarted := make(chan struct{})
	pullDone := make(chan struct{})
	mockPuller := &mockImagePuller{
		pullFn: func(ctx context.Context, img string) error {
			close(pullStarted)
			<-pullDone
			return nil
		},
	}

	onCompleteCalled := make(chan error, 1)
	svc := NewImageUpdateService(mockDB, mockPuller,
		WithOnPullComplete(func(poolID int64, err error) {
			onCompleteCalled <- err
		}),
	)

	// Flag an available update first
	up := svc.FlagUpdate(42, testImage, bumpedDigest)
	if up.Status != "available" {
		t.Fatalf("expected status available, got %s", up.Status)
	}

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	// Call PullImage (triggers background pull)
	pullRes, err := client.PullImage(ctx, connect.NewRequest(&supervisorv1.PullImageRequest{PoolId: 42}))
	if err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}
	if !pullRes.Msg.Success {
		t.Fatalf("expected success=true from PullImage")
	}

	// Wait until puller goroutine starts
	select {
	case <-pullStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for pull to start")
	}

	// Status while pulling should be "pulling"
	listRes, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{PoolId: 42}))
	if err != nil {
		t.Fatalf("ListImageUpdates failed: %v", err)
	}
	if len(listRes.Msg.Updates) != 1 {
		t.Fatalf("expected 1 update while pulling, got %d", len(listRes.Msg.Updates))
	}
	if listRes.Msg.Updates[0].Status != "pulling" {
		t.Errorf("expected status=pulling, got %s", listRes.Msg.Updates[0].Status)
	}

	// Allow pull to complete
	close(pullDone)

	select {
	case err := <-onCompleteCalled:
		if err != nil {
			t.Fatalf("expected nil error on complete, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for onComplete callback")
	}

	// After completion, update is resolved / deleted
	listResAfter, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{PoolId: 42}))
	if err != nil {
		t.Fatalf("ListImageUpdates after pull failed: %v", err)
	}
	if len(listResAfter.Msg.Updates) != 0 {
		t.Errorf("expected 0 updates after pull completion, got %d", len(listResAfter.Msg.Updates))
	}
}

func TestImageUpdate_BackgroundPullFailure(t *testing.T) {
	ctx := context.Background()
	testImage := "ghcr.io/noosxe/runnero:v1.2.0"

	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			77: {ID: 77, Name: "pool-fail", RunnerImage: testImage},
		},
	}

	mockPuller := &mockImagePuller{
		pullFn: func(ctx context.Context, img string) error {
			return errors.New("network timeout during layer download")
		},
	}

	onCompleteCalled := make(chan error, 1)
	svc := NewImageUpdateService(mockDB, mockPuller,
		WithOnPullComplete(func(poolID int64, err error) {
			onCompleteCalled <- err
		}),
	)

	svc.FlagUpdate(77, testImage, "sha256:fail")

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	// Trigger background pull
	_, err := client.PullImage(ctx, connect.NewRequest(&supervisorv1.PullImageRequest{PoolId: 77}))
	if err != nil {
		t.Fatalf("PullImage request failed: %v", err)
	}

	// Wait for completion callback
	select {
	case pullErr := <-onCompleteCalled:
		if pullErr == nil {
			t.Fatalf("expected pull error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for onComplete callback")
	}

	// Update status must be reverted to "available" so admin can retry
	listRes, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{PoolId: 77}))
	if err != nil {
		t.Fatalf("ListImageUpdates failed: %v", err)
	}
	if len(listRes.Msg.Updates) != 1 {
		t.Fatalf("expected 1 update reverted, got %d", len(listRes.Msg.Updates))
	}
	if listRes.Msg.Updates[0].Status != "available" {
		t.Errorf("expected status reverted to available, got %s", listRes.Msg.Updates[0].Status)
	}
}

func TestImageUpdate_DismissImageUpdate(t *testing.T) {
	ctx := context.Background()
	testImage := "ghcr.io/noosxe/runnero:v1.3.0"

	mockDB := &mockImageUpdateDB{
		pools: map[int64]db.RunnerPool{
			88: {ID: 88, Name: "pool-dismiss", RunnerImage: testImage},
		},
	}
	mockPuller := &mockImagePuller{}
	svc := NewImageUpdateService(mockDB, mockPuller)

	up := svc.FlagUpdate(88, testImage, "sha256:dismiss-digest")

	mux := http.NewServeMux()
	path, handler := supervisorv1connect.NewImageUpdateServiceHandler(svc, BinaryConnectHandlerOptions()...)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := supervisorv1connect.NewImageUpdateServiceClient(srv.Client(), srv.URL)

	// Dismiss notification (simple acknowledge)
	dismissRes, err := client.DismissImageUpdate(ctx, connect.NewRequest(&supervisorv1.DismissImageUpdateRequest{
		Id: up.Id,
	}))
	if err != nil {
		t.Fatalf("DismissImageUpdate failed: %v", err)
	}
	if !dismissRes.Msg.Success {
		t.Errorf("expected dismiss success=true")
	}

	// Verify dismissed notification no longer appears in pending list
	listRes, err := client.ListImageUpdates(ctx, connect.NewRequest(&supervisorv1.ListImageUpdatesRequest{PoolId: 88}))
	if err != nil {
		t.Fatalf("ListImageUpdates failed: %v", err)
	}
	if len(listRes.Msg.Updates) != 0 {
		t.Errorf("expected 0 pending updates after dismiss, got %d", len(listRes.Msg.Updates))
	}

	// Verify dismissing unknown ID returns NotFound
	_, err = client.DismissImageUpdate(ctx, connect.NewRequest(&supervisorv1.DismissImageUpdateRequest{
		Id: 99999,
	}))
	if err == nil {
		t.Fatalf("expected error dismissing nonexistent update, got nil")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %v", connect.CodeOf(err))
	}
}

