package orchestrator

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/webhook"
)

// NormalizeRepositoryURL cleans and normalizes a repository or organization URL
// by stripping trailing slashes, stripping .git extensions, and lowercasing scheme/host/path.
func NormalizeRepositoryURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		cleaned := strings.ToLower(rawURL)
		cleaned = strings.TrimSuffix(cleaned, "/")
		cleaned = strings.TrimSuffix(cleaned, ".git")
		return strings.TrimSuffix(cleaned, "/")
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	path := strings.TrimSuffix(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	u.Path = strings.ToLower(path)
	u.RawQuery = ""
	u.Fragment = ""

	return strings.TrimSuffix(u.String(), "/")
}

// LabelsMatch checks whether a runner pool configured with poolLabelsRaw provides
// all the labels required by the workflow job. Delegates to the shared provider
// implementation (docs/24 §5.2) so the webhook fast path and demand polling
// apply identical label semantics.
func LabelsMatch(poolLabelsRaw string, requiredLabels []string) bool {
	return provider.LabelsMatch(poolLabelsRaw, requiredLabels)
}

// MatchPoolForEvent finds the most specific matching runner pool for a webhook event.
// Pools are evaluated based on provider, label compatibility, and URL scope hierarchy (repo > org > global).
func MatchPoolForEvent(pools []db.RunnerPool, providerName string, event *webhook.WorkflowJobEvent) *db.RunnerPool {
	p, _ := MatchPoolForEventWithTargets(pools, nil, providerName, event)
	return p
}

// MatchPoolForEventWithTargets finds the most specific matching runner pool and target URL for a webhook event.
// poolTargets optionally maps pool ID to configured targets from pool_targets table.
func MatchPoolForEventWithTargets(pools []db.RunnerPool, poolTargets map[int64][]string, providerName string, event *webhook.WorkflowJobEvent) (*db.RunnerPool, string) {
	if event == nil || len(pools) == 0 {
		return nil, ""
	}

	pName := strings.ToLower(strings.TrimSpace(providerName))

	var candidateURLs []string
	if event.Repository.HTMLURL != "" {
		candidateURLs = append(candidateURLs, NormalizeRepositoryURL(event.Repository.HTMLURL))
	}
	if event.Repository.CloneURL != "" {
		candidateURLs = append(candidateURLs, NormalizeRepositoryURL(event.Repository.CloneURL))
	}
	if event.Repository.FullName != "" && pName == "github" {
		candidateURLs = append(candidateURLs, NormalizeRepositoryURL("https://github.com/"+event.Repository.FullName))
	}

	fullNameLower := strings.ToLower(strings.TrimSpace(event.Repository.FullName))

	var bestPool *db.RunnerPool
	bestTargetURL := ""
	bestScore := -1

	for i := range pools {
		p := &pools[i]
		if strings.ToLower(strings.TrimSpace(p.Provider)) != pName {
			continue
		}

		if !LabelsMatch(p.Labels, event.WorkflowJob.Labels) {
			continue
		}

		scope := strings.ToLower(strings.TrimSpace(p.Scope))
		if scope == "" {
			scope = "repo"
		}

		targets := poolTargets[p.ID]
		if len(targets) == 0 && p.RepositoryUrl != "" {
			targets = []string{p.RepositoryUrl}
		}

		for _, rawTarget := range targets {
			targetURL := NormalizeRepositoryURL(rawTarget)
			score := -1
			switch scope {
			case "repo":
				matched := false
				for _, cURL := range candidateURLs {
					if targetURL == cURL {
						matched = true
						break
					}
				}
				if !matched && fullNameLower != "" && strings.HasSuffix(targetURL, "/"+fullNameLower) {
					matched = true
				}
				if matched {
					score = 300
				}

			case "org":
				// targetURL represents an organization, e.g. https://github.com/my-org
				matched := false
				for _, cURL := range candidateURLs {
					if strings.HasPrefix(cURL, targetURL+"/") {
						matched = true
						break
					}
				}
				if !matched && fullNameLower != "" {
					orgPart := fullNameLower
					if slashIdx := strings.Index(fullNameLower, "/"); slashIdx != -1 {
						orgPart = fullNameLower[:slashIdx]
					}
					if strings.HasSuffix(targetURL, "/"+orgPart) {
						matched = true
					}
				}
				if matched {
					score = 200
				}

			case "global":
				// targetURL is the instance root, e.g. https://github.com or https://gitea.example.com
				matched := false
				if poolParsed, err := url.Parse(targetURL); err == nil && poolParsed.Host != "" {
					for _, cURL := range candidateURLs {
						if cParsed, err := url.Parse(cURL); err == nil && cParsed.Host != "" {
							if strings.EqualFold(poolParsed.Host, cParsed.Host) {
								matched = true
								break
							}
						}
					}
				}
				if matched {
					score = 100
				}
			}

			if score > bestScore {
				bestScore = score
				bestPool = p
				bestTargetURL = rawTarget
			}
		}
	}

	if bestPool != nil && bestTargetURL == "" {
		bestTargetURL = bestPool.RepositoryUrl
	}

	return bestPool, bestTargetURL
}

// HandleWorkflowJob implements webhook.EventHandler.
// On "queued" action:
//  1. Matches pool by repository URL (+ scope) and label compatibility
//  2. Books the job as pending demand and serves it from warm idle runners
//     registered against the matched target where possible (warm-first,
//     docs/03 §4); only the uncovered shortfall provisions a new runner
//     without waiting for the periodic audit tick
//  3. Verifies active_runners < max_concurrency
//  4. Verifies global runner quota (circuit breaker); enqueues internally if saturated
func (c *PoolController) HandleWorkflowJob(ctx context.Context, providerName string, event *webhook.WorkflowJobEvent) error {
	if event == nil {
		return nil
	}

	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()

	if state == StateStopped {
		return ErrControllerStopped
	}
	if state == StatePaused {
		c.logger.Info("controller paused, ignoring queued webhook event", "provider", providerName, "action", event.Action)
		return nil
	}

	switch event.Action {
	case "queued":
		pools, err := c.loadPools(ctx)
		if err != nil {
			return fmt.Errorf("loading pools for webhook event: %w", err)
		}

		poolTargetsMap := make(map[int64][]string, len(pools))
		for _, p := range pools {
			poolTargetsMap[p.ID] = c.loadPoolTargets(ctx, p)
		}

		targetPool, matchedTargetURL := MatchPoolForEventWithTargets(pools, poolTargetsMap, providerName, event)
		if targetPool == nil {
			c.logger.Info("no matching pool found for queued webhook event",
				"provider", providerName,
				"repo", event.Repository.FullName,
				"job_id", event.WorkflowJob.ID,
				"requested_labels", event.WorkflowJob.Labels,
			)
			return nil
		}
		// Webhook enrichment (docs/21 §5.5): upsert the queued row keyed by the
		// external job id before any capacity gating — the job is queued
		// regardless of whether this deployment can spawn a runner for it.
		c.recordWebhookQueued(ctx, targetPool, event)

		// Book the job as pending demand for this pool before any capacity
		// decision: repeated queued events must see the demand booked by their
		// predecessors, or a burst would each observe the same warm runner and
		// under-provision (docs/03 §4, warm-first provisioning).
		c.demand.markQueued(targetPool.ID, event.WorkflowJob.ID)
		pendingDemand := int64(c.demand.pending(targetPool.ID))

		// Fast capacity check before acquiring single-writer provisioning lock.
		// Warm-runner-first: an idle runner registered against the matched
		// target is capacity the forge can assign the job to immediately, so
		// only the demand not covered by such runners may spawn (the same
		// deficit principle as the polling path, RUN-151). Runners with no
		// known target (legacy adoptions) are not attributable and never mask
		// demand, matching idleByTarget accounting.
		matchedNormalized := NormalizeRepositoryURL(matchedTargetURL)
		tracked := c.reconciler.TrackedPoolRunners(targetPool.ID)
		activeCount := int64(0)
		idleOnTarget := int64(0)
		for _, r := range tracked {
			if r.State != "running" {
				continue
			}
			activeCount++
			if !r.IsBusy && r.TargetURL != "" && NormalizeRepositoryURL(r.TargetURL) == matchedNormalized {
				idleOnTarget++
			}
		}
		if pendingDemand <= idleOnTarget {
			c.logger.Info("queued webhook covered by warm idle runner, skipping spawn",
				"pool", targetPool.Name,
				"job_id", event.WorkflowJob.ID,
				"pending_demand", pendingDemand,
				"idle_on_target", idleOnTarget,
			)
			return nil
		}
		// Per-pool max_concurrency check
		if targetPool.MaxConcurrency > 0 && activeCount >= targetPool.MaxConcurrency {
			c.logger.Info("pool reached max concurrency limit, skipping queued event spawn",
				"pool", targetPool.Name,
				"active", activeCount,
				"max_concurrency", targetPool.MaxConcurrency,
				"job_id", event.WorkflowJob.ID,
			)
			return nil
		}

		// Global quota circuit breaker check
		if c.globalMaxRunners > 0 && c.TotalActiveRunners() >= c.globalMaxRunners {
			c.logger.Warn("global runner quota saturated on queued webhook, queuing request internally",
				"pool", targetPool.Name,
				"global_active", c.TotalActiveRunners(),
				"global_max", c.globalMaxRunners,
				"job_id", event.WorkflowJob.ID,
			)
			c.enqueueRequest(*targetPool, matchedTargetURL)
			return nil
		}

		c.provisionMu.Lock()
		defer c.provisionMu.Unlock()

		// Re-check capacity under lock to prevent race conditions
		tracked = c.reconciler.TrackedPoolRunners(targetPool.ID)
		activeCount = 0
		idleOnTarget = 0
		for _, r := range tracked {
			if r.State != "running" {
				continue
			}
			activeCount++
			if !r.IsBusy && r.TargetURL != "" && NormalizeRepositoryURL(r.TargetURL) == matchedNormalized {
				idleOnTarget++
			}
		}
		// Another writer may have provisioned for the same demand while we
		// waited on the lock; re-run the warm-first shortfall check too.
		if int64(c.demand.pending(targetPool.ID)) <= idleOnTarget {
			c.logger.Info("queued webhook covered by warm idle runner under lock, skipping spawn",
				"pool", targetPool.Name,
				"job_id", event.WorkflowJob.ID,
				"idle_on_target", idleOnTarget,
			)
			return nil
		}
		if targetPool.MaxConcurrency > 0 && activeCount >= targetPool.MaxConcurrency {
			c.logger.Info("pool reached max concurrency under lock, skipping queued event spawn",
				"pool", targetPool.Name,
				"active", activeCount,
				"max_concurrency", targetPool.MaxConcurrency,
			)
			return nil
		}
		if c.globalMaxRunners > 0 && c.TotalActiveRunners() >= c.globalMaxRunners {
			c.logger.Warn("global quota saturated under lock, queuing request internally",
				"pool", targetPool.Name,
				"global_active", c.TotalActiveRunners(),
				"global_max", c.globalMaxRunners,
			)
			c.enqueueRequest(*targetPool, matchedTargetURL)
			return nil
		}

		if err := c.spawnSingleRunner(ctx, *targetPool, nil, true, matchedTargetURL); err != nil {
			c.logger.Error("failed spawning runner for queued webhook event",
				"pool", targetPool.Name,
				"job_id", event.WorkflowJob.ID,
				"target", matchedTargetURL,
				"err", err,
			)
			return fmt.Errorf("spawning runner for pool %q on queued event: %w", targetPool.Name, err)
		}

		c.logger.Info("provisioned runner immediately from queued webhook event",
			"pool", targetPool.Name,
			"job_id", event.WorkflowJob.ID,
			"repo", event.Repository.FullName,
		)
		return nil

	case "in_progress":
		if event.WorkflowJob.RunnerName != "" && c.reconciler != nil {
			c.reconciler.MarkRunnerBusy(event.WorkflowJob.RunnerName, true)
		}
		// Webhook enrichment (docs/21 §5.5): attach the external job identity
		// and timestamps to the job's open row, applying the merge rules so
		// queued stubs, poll-opened transition rows, and this event never
		// produce duplicate records.
		if p := c.resolveEventPool(ctx, providerName, event); p != nil {
			c.demand.markStarted(p.ID, event.WorkflowJob.ID)
			// Standby backfill (docs/03 §4): the runner just went busy, so its
			// min_idle slot is free — provision the replacement now rather than
			// letting the next queued job (or a reconcile tick) pay for latency.
			c.replenishIdleStandby(ctx, p, event.WorkflowJob.RunnerName)
			c.recordWebhookEvent(ctx, "in_progress", p, event)
		}
		return nil

	case "completed":
		if event.WorkflowJob.RunnerName != "" && c.reconciler != nil {
			c.reconciler.MarkRunnerBusy(event.WorkflowJob.RunnerName, false)
		}
		// Webhook enrichment (docs/21 §5.5): close the job's open row with the
		// forge-provided conclusion and completed_at.
		if p := c.resolveEventPool(ctx, providerName, event); p != nil {
			c.demand.markFinished(p.ID, event.WorkflowJob.ID)
			c.recordWebhookEvent(ctx, "completed", p, event)
		}
		return nil

	default:
		return nil
	}
}

// resolveEventPool finds the pool a webhook event belongs to using the same

// replenishIdleStandby backfills the warm standby slot a runner vacated when
// its job started (docs/03 §3/§4): min_idle counts idle standbys ready for
// dispatch, so the moment one is consumed the pool provisions a replacement —
// subject to the same capacity constraints (per-pool max_concurrency, global
// quota) as every other spawn. The reconcile loop performs the same top-up
// (busy-inclusive effective target); this webhook hook removes the tick
// latency so the next queued job finds a warm runner instead of waiting for
// a cold spawn. Best-effort: failures are logged and left for the reconcile
// loop to retry.
func (c *PoolController) replenishIdleStandby(ctx context.Context, p *db.RunnerPool, runnerName string) {
	if p == nil || p.MinIdleRunners <= 0 || c.reconciler == nil {
		return
	}

	// Refill the slot on the target the busy runner was serving; legacy
	// adoptions carry no target, so fall back to pool-wide idle accounting
	// and the pool's default target.
	backfillTarget := ""
	for _, r := range c.reconciler.TrackedPoolRunners(p.ID) {
		if r.Name == runnerName && r.TargetURL != "" {
			backfillTarget = r.TargetURL
			break
		}
	}
	var backfillNormalized string
	if backfillTarget != "" {
		backfillNormalized = NormalizeRepositoryURL(backfillTarget)
	}

	// idleDeficit returns (idle, active) — per-target accounting when the
	// vacated target is known, pool-wide otherwise.
	idleDeficit := func() (idle, active int64) {
		for _, r := range c.reconciler.TrackedPoolRunners(p.ID) {
			if r.State != "running" {
				continue
			}
			active++
			if r.IsBusy {
				continue
			}
			if backfillNormalized == "" {
				idle++ // pool-wide accounting for unattributed runners
				continue
			}
			if r.TargetURL != "" && NormalizeRepositoryURL(r.TargetURL) == backfillNormalized {
				idle++
			}
		}
		return idle, active
	}

	idle, active := idleDeficit()
	if idle >= int64(p.MinIdleRunners) {
		return // standby buffer already full
	}
	if p.MaxConcurrency > 0 && active >= p.MaxConcurrency {
		c.logger.Info("pool at max_concurrency, deferring idle replenishment to reconcile",
			"pool", p.Name,
			"active", active,
			"max_concurrency", p.MaxConcurrency,
		)
		return
	}
	if c.globalMaxRunners > 0 && c.TotalActiveRunners() >= c.globalMaxRunners {
		c.logger.Warn("global runner quota saturated, queuing idle replenishment internally",
			"pool", p.Name,
			"global_active", c.TotalActiveRunners(),
			"global_max", c.globalMaxRunners,
		)
		c.enqueueRequest(*p, backfillTarget)
		return
	}

	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	// Re-check under lock: another writer may have replenished meanwhile.
	idle, active = idleDeficit()
	if idle >= int64(p.MinIdleRunners) {
		return
	}
	if p.MaxConcurrency > 0 && active >= p.MaxConcurrency {
		return
	}
	if c.globalMaxRunners > 0 && c.TotalActiveRunners() >= c.globalMaxRunners {
		c.enqueueRequest(*p, backfillTarget)
		return
	}

	if err := c.spawnSingleRunner(ctx, *p, nil, false, backfillTarget); err != nil {
		// Non-fatal: the reconcile loop's busy-inclusive target retries this
		// on the next cycle, so the buffer self-heals.
		c.logger.Warn("idle replenishment spawn failed",
			"pool", p.Name,
			"target", backfillTarget,
			"err", err,
		)
		return
	}

	c.logger.Info("replenished idle standby after runner pickup",
		"pool", p.Name,
		"runner", runnerName,
		"target", backfillTarget,
		"idle_before", idle,
		"min_idle", p.MinIdleRunners,
	)
}

// repository/scope/label matching as the queued spawn path. Returns nil when
// no pool matches — recording is scoped to configured pools only.
func (c *PoolController) resolveEventPool(ctx context.Context, providerName string, event *webhook.WorkflowJobEvent) *db.RunnerPool {
	pools, err := c.loadPools(ctx)
	if err != nil {
		c.logger.Warn("loading pools for webhook job recording failed", "err", err)
		return nil
	}

	poolTargetsMap := make(map[int64][]string, len(pools))
	for _, p := range pools {
		poolTargetsMap[p.ID] = c.loadPoolTargets(ctx, p)
	}

	p, _ := MatchPoolForEventWithTargets(pools, poolTargetsMap, providerName, event)
	return p
}

// webhookJobMeta extracts the forge-provided job identity for enrichment.
func webhookJobMeta(event *webhook.WorkflowJobEvent) db.WebhookJobMeta {
	return db.WebhookJobMeta{
		RunID:        event.WorkflowJob.RunID,
		WorkflowName: event.WorkflowJob.WorkflowName,
		HeadBranch:   event.WorkflowJob.HeadBranch,
		HeadSHA:      event.WorkflowJob.HeadSHA,
	}
}

// parseWebhookTime parses a forge-provided RFC3339 timestamp leniently:
// empty or invalid values yield the zero time, which the recorder maps to a
// NULL column (truthful degradation, docs/21 §5.6).
func parseWebhookTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// webhookConclusionStatus maps a forge conclusion to the job_history status
// vocabulary (docs/21 §5.5): the three real outcomes keep their names,
// everything else (including empty) closes as 'completed'.
func webhookConclusionStatus(conclusion string) string {
	switch conclusion {
	case "success", "failure", "cancelled":
		return conclusion
	default:
		return "completed"
	}
}

// recordWebhookEvent dispatches a webhook event to the job-history recorder
// (docs/21 §5.5). Best-effort: recording failures are logged and never fail
// recordWebhookQueued upserts the queued webhook row for a matched pool
// (docs/21 §5.5). Best-effort: failures are logged and never block spawning.
func (c *PoolController) recordWebhookQueued(ctx context.Context, p *db.RunnerPool, event *webhook.WorkflowJobEvent) {
	if c.jobRecorder == nil || event.WorkflowJob.ID == 0 {
		return
	}
	if err := c.jobRecorder.RecordWebhookQueued(ctx, p.ID, event.WorkflowJob.ID, webhookJobMeta(event), parseWebhookTime(event.WorkflowJob.CreatedAt)); err != nil {
		c.logger.Warn("webhook job recording failed",
			"pool", p.Name, "action", "queued", "job_id", event.WorkflowJob.ID, "err", err)
	}
}

// recordWebhookEvent dispatches a webhook event to the job-history recorder
func (c *PoolController) recordWebhookEvent(ctx context.Context, action string, p *db.RunnerPool, event *webhook.WorkflowJobEvent) {
	if c.jobRecorder == nil || event.WorkflowJob.ID == 0 {
		return
	}

	var err error
	switch action {
	case "in_progress":
		err = c.jobRecorder.RecordWebhookStarted(
			ctx, p.ID, event.WorkflowJob.ID, event.WorkflowJob.RunnerName,
			parseWebhookTime(event.WorkflowJob.StartedAt), parseWebhookTime(event.WorkflowJob.CreatedAt),
			webhookJobMeta(event))
	case "completed":
		err = c.jobRecorder.RecordWebhookCompleted(
			ctx, p.ID, event.WorkflowJob.ID, event.WorkflowJob.RunnerName,
			webhookConclusionStatus(event.WorkflowJob.Conclusion),
			parseWebhookTime(event.WorkflowJob.CompletedAt))
	}
	if err != nil {
		c.logger.Warn("webhook job recording failed",
			"pool", p.Name, "action", action, "job_id", event.WorkflowJob.ID, "err", err)
	}
}
