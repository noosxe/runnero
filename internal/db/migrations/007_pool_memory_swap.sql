-- +goose Up
-- +goose StatementBegin

-- RUN-147: configurable per-pool memory swap cap.
-- Semantics mirror Docker's HostConfig.MemorySwap (the TOTAL memory+swap
-- allowance): NULL = unset (daemon default of 2x memory — preserves the
-- pre-RUN-147 behavior for existing pools), '-1' = unlimited swap, otherwise
-- a memory string (e.g. '8GB') that must be >= memory_limit; equal to
-- memory_limit means the runner gets no extra swap.
ALTER TABLE runner_pools ADD COLUMN memory_swap_limit TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE runner_pools DROP COLUMN memory_swap_limit;
-- +goose StatementEnd
