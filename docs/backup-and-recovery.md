# Backup & Disaster Recovery

The AIO Supervisor persists its critical state (encrypted credentials, runner pool topologies, job history, and application settings) in an embedded SQLite database. This document defines the automated snapshot architecture, retention mechanisms, on-demand backup CLI, and the step-by-step disaster recovery procedure per [Open Question #21](file:///home/mechsoull/Projects/runnero/docs/open-questions.md#21-backup--disaster-recovery).

---

## 1. Automated Snapshot Architecture

- **Mechanism**: The supervisor executes online SQLite snapshots via `VACUUM INTO '<backup_path>'`. This creates a transactionally consistent, page-compacted snapshot of the live database file without blocking concurrent queries or write operations.
- **Interval**: Automated snapshots run periodically every **6 hours** by default.
  - Configurable via the `SUPERVISOR_BACKUP_INTERVAL_HOURS` environment variable (env-var-only configuration, not exposed in the Web UI).
- **Location**: Snapshots are saved to:
  ```text
  <DATA_DIR>/backups/supervisor-<timestamp>.db
  ```
  Example: `/data/backups/supervisor-20260903-004500.db` (container) or `/var/lib/runnero/backups/supervisor-20260903-004500.db` (NixOS module, docs/39 — the data dir is fixed to `/var/lib/runnero` there).
- **Retention**: By default, the supervisor retains the newest **7** snapshots.
  - Older snapshots are automatically pruned after each snapshot creation.
  - Configurable via the `SUPERVISOR_BACKUP_RETENTION_COUNT` environment variable.

---

## 2. On-Demand CLI Backup

Administrators can trigger an on-demand snapshot at any time using the `supervisor backup` command:

```bash
# Inside the container:
supervisor backup

# Or via docker exec on the host:
docker exec -it supervisor supervisor backup

# NixOS module (docs/39): runs as the runnero user with the service's
# config and environment file
sudo runnero-admin backup
```

Output:
```text
Successfully created backup snapshot at /data/backups/supervisor-20260903-120000.db
```

### Back up the encryption key separately

Every stored credential (PATs, GitHub App keys, forge tokens) is encrypted with a key derived from `SUPERVISOR_DB_ENCRYPTION_KEY`. Snapshots are useless without it, and losing it means re-entering every credential. Keep a copy of the key **outside** the data dir and its backups — in the secret manager that provisions it (sops-nix/agenix secrets in your configuration repository, a password manager, …) — and never store it next to the database: a key in the same backup as the data it protects defeats the encryption for anyone holding that backup. On NixOS the key lives in the module's `environmentFile` (outside `/var/lib/runnero` by design); in the container it lives in the `.env` file next to `docker-compose.yml`.

---

## 3. Disaster Recovery Procedure

If the SQLite database file becomes corrupted (e.g. host kernel crash, ungraceful power loss, or storage hardware fault), the supervisor's boot integrity check (`PRAGMA quick_check;`) will fail with:

```text
ERROR database corrupted path=/data/supervisor.db err="database file is corrupted: file is not a database (26); refuse to start; restore from a backup in DATA_DIR/backups (OQ #21)"
```

Follow this procedure to restore the database from a backup snapshot. Paths below use the container data dir `/data`; on the NixOS module substitute `/var/lib/runnero` and run the commands as root.

### Step 1: Stop the Supervisor
Stop the supervisor container so no processes access `/data`:
```bash
docker stop supervisor
# Or with docker compose:
docker compose stop supervisor
# Or on the NixOS module:
systemctl stop runnero
```

### Step 2: Identify the Latest Valid Snapshot
List available snapshots in the backup volume/directory:
```bash
ls -lt /data/backups/supervisor-*.db
```

Verify the chosen snapshot's integrity with the SQLite CLI:
```bash
sqlite3 /data/backups/supervisor-20260903-004500.db "PRAGMA quick_check;"
# Expected output: ok
```

### Step 3: Replace the Corrupted Database
Replace the corrupted database file with the verified snapshot and remove any stale SQLite WAL / shared-memory files:
```bash
# Copy snapshot over the primary database
cp /data/backups/supervisor-20260903-004500.db /data/supervisor.db

# Remove stale WAL / SHM journal files if present
rm -f /data/supervisor.db-wal /data/supervisor.db-shm
```

On the NixOS module, keep the restored file owned by the service user: `install -o runnero -g runnero -m 0640 <snapshot> /var/lib/runnero/supervisor.db`.

### Step 4: Restart the Supervisor
Start the supervisor container:
```bash
docker start supervisor
# Or with docker compose:
docker compose up -d supervisor
# Or on the NixOS module:
systemctl start runnero
```

### Step 5: Verify Health
Confirm the supervisor has booted successfully and the database health probe reports `ok`:
```bash
curl -s http://localhost:8090/healthz
# Expected response:
# {"status":"healthy","checks":{"db":"ok"}}
```
