import { PoolHealthStatus, type Pool } from "../../gen/api_pb";
import { AlertTriangle, ArrowRight, KeyRound, ShieldAlert, Wrench } from "lucide-react";
import { Link } from "@tanstack/react-router";

export interface PoolDiagnosticsCardProps {
  pool: Pool;
  className?: string;
}

function getRemediationInfo(errorCode?: string): {
  suggestion: string;
  actionText?: string;
  actionHref?: string;
} {
  switch (errorCode) {
    case "AUTH_DECRYPTION_FAILED":
      return {
        suggestion:
          "The supervisor master encryption key could not decrypt the Git credentials for this pool. Re-enter the private key or token in Git Auth Profiles to update ciphertext.",
        actionText: "Fix in Git Auth Profiles",
        actionHref: "/profiles",
      };
    case "PROVIDER_AUTH_FAILED":
      return {
        suggestion:
          "The upstream Git provider rejected credentials with 401/403 Unauthorized. Verify your GitHub App private key or Personal Access Token.",
        actionText: "Check Auth Profile",
        actionHref: "/profiles",
      };
    case "TARGET_NOT_FOUND":
      return {
        suggestion:
          "The target repository or organization URL returned 404 Not Found. Verify the URL and ensure the GitHub App or token has permission to access it.",
        actionText: "Verify Profiles & Permissions",
        actionHref: "/profiles",
      };
    case "REGISTRATION_TOKEN_FAILED":
      return {
        suggestion:
          "Failed to acquire a short-lived runner registration token from the Git provider. Check API rate limits or runner admin permissions.",
        actionText: "View Auth Profiles",
        actionHref: "/profiles",
      };
    case "DOCKER_ENGINE_ERROR":
      return {
        suggestion:
          "Container engine error during runner provisioning. Verify the Docker socket mount, host disk storage, and container memory limits.",
      };
    case "IMAGE_PULL_FAILED":
      return {
        suggestion:
          "The runner container image could not be pulled from the container registry. Verify the image tag or configure registry credentials.",
      };
    case "GLOBAL_QUOTA_SATURATED":
      return {
        suggestion:
          "The global supervisor runner limit has been saturated. Wait for active runners to finish or increase Total Allowed Runners in Global Constraints.",
        actionText: "Adjust Global Quotas",
        actionHref: "/settings",
      };
    default:
      return {
        suggestion:
          "Reconciliation failed during provisioning. Review the error details and ensure container host and Git credentials are functional.",
      };
  }
}

function formatTimestamp(isoString?: string): string {
  if (!isoString) return "";
  try {
    const d = new Date(isoString);
    if (isNaN(d.getTime())) return "";
    return d.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch {
    return "";
  }
}

export function PoolDiagnosticsCard({ pool, className = "" }: PoolDiagnosticsCardProps) {
  // Only render if pool is degraded or has an active reconciliation error
  if (pool.healthStatus !== PoolHealthStatus.DEGRADED && !pool.lastError) {
    return null;
  }

  const remediation = getRemediationInfo(pool.lastErrorCode);
  const formattedTime = formatTimestamp(pool.lastErrorTimestamp);

  return (
    <div
      className={`rounded-2xl border border-rose-300 bg-rose-50/70 p-5 shadow-xs dark:border-rose-900/60 dark:bg-rose-950/30 ${className}`}
    >
      <div className="flex items-start gap-3.5">
        <div className="mt-0.5 rounded-xl bg-rose-100 p-2 text-rose-600 dark:bg-rose-900/50 dark:text-rose-400">
          <AlertTriangle className="h-5 w-5" />
        </div>

        <div className="flex-1 space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-bold text-rose-900 dark:text-rose-200">
              Reconciliation Failure Alert
            </h3>
            {pool.lastErrorCode && (
              <span className="inline-flex items-center rounded-md bg-rose-100 px-2 py-0.5 font-mono text-[11px] font-semibold text-rose-800 dark:bg-rose-900/60 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
                {pool.lastErrorCode}
              </span>
            )}
            {formattedTime && (
              <span className="text-xs text-rose-600/80 dark:text-rose-400/80">
                • Occurred at {formattedTime}
              </span>
            )}
          </div>

          <div className="rounded-xl border border-rose-200/80 bg-white/80 p-3 text-xs font-mono text-rose-800 dark:border-rose-900/40 dark:bg-slate-900/80 dark:text-rose-300 break-words">
            {pool.lastError || "Unknown reconciliation error encountered"}
          </div>

          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between pt-1">
            <div className="flex items-start gap-1.5 text-xs text-rose-700 dark:text-rose-300/90">
              <Wrench className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{remediation.suggestion}</span>
            </div>

            {remediation.actionHref && (
              <Link
                to={remediation.actionHref}
                className="inline-flex shrink-0 items-center gap-1 rounded-xl bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs transition-colors hover:bg-rose-500 dark:bg-rose-500 dark:hover:bg-rose-600"
              >
                {remediation.actionHref === "/profiles" ? (
                  <KeyRound className="h-3.5 w-3.5" />
                ) : (
                  <ShieldAlert className="h-3.5 w-3.5" />
                )}
                <span>{remediation.actionText}</span>
                <ArrowRight className="h-3.5 w-3.5" />
              </Link>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
