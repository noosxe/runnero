-- +goose Up
-- +goose StatementBegin

-- Demand polling fallback (docs/24, RUN-145): per-pool opt-in for webhook-driven
-- pools (GitHub) to scale without inbound webhooks by polling repo targets for
-- queued jobs. Defaults preserve today's behavior for every existing pool.
ALTER TABLE runner_pools ADD COLUMN poll_fallback BOOLEAN NOT NULL DEFAULT 0;
-- Cadence of the demand poll in seconds (docs/24 §5.4). CHECK range matches the
-- RPC validation clamp; zero is impossible via the RPC path (0 maps to 30) and
-- is treated by the controller as "no throttle" for legacy/test rows only.
ALTER TABLE runner_pools ADD COLUMN poll_interval_seconds INTEGER NOT NULL DEFAULT 30
    CHECK (poll_interval_seconds = 0 OR (poll_interval_seconds >= 15 AND poll_interval_seconds <= 3600));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE runner_pools DROP COLUMN poll_interval_seconds;
ALTER TABLE runner_pools DROP COLUMN poll_fallback;
-- +goose StatementEnd
