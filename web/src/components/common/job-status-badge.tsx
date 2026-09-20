import {
  CheckCircle2,
  CircleSlash,
  Clock,
  LoaderCircle,
  TimerOff,
  XCircle,
  ZapOff,
} from "lucide-react";
import { cn } from "cn";

import { Badge } from "@/components/ui/badge";

/**
 * Job status badge: one component renders the job lifecycle vocabulary
 * everywhere — dashboard Recent Executions, history table, job detail
 * header, renovate run list.
 *
 * The vocabulary is fixed by the database CHECK constraint
 * (internal/db/migrations/004_job_history_lifecycle.sql): queued, running,
 * success, failure, cancelled, timeout, completed, interrupted. Semantics
 * (docs/21 §5): success/failure/cancelled are authoritative forge
 * conclusions; completed = clean exit without an authoritative conclusion;
 * interrupted = the supervisor tore the runner down mid-job.
 *
 * Colors: success → success (green), failure/timeout → destructive (red),
 * completed/cancelled → muted gray, queued → warning (amber),
 * running → primary (blue), interrupted → notice (violet).
 */
type JobStatusMeta = {
  label: string;
  icon: typeof CheckCircle2;
  className: string;
};

const JOB_STATUS_META: Record<string, JobStatusMeta> = {
  success: {
    label: "success",
    icon: CheckCircle2,
    className: "border-success/30 bg-success/10 text-success",
  },
  failure: {
    label: "failure",
    icon: XCircle,
    className: "border-destructive/30 bg-destructive/10 text-destructive",
  },
  timeout: {
    label: "timeout",
    icon: TimerOff,
    className: "border-destructive/30 bg-destructive/10 text-destructive",
  },
  completed: {
    label: "completed",
    icon: CheckCircle2,
    className: "bg-muted text-muted-foreground",
  },
  cancelled: {
    label: "cancelled",
    icon: CircleSlash,
    className: "bg-muted text-muted-foreground",
  },
  queued: {
    label: "queued",
    icon: Clock,
    className: "border-warning/30 bg-warning/10 text-warning",
  },
  running: {
    label: "running",
    icon: LoaderCircle,
    className: "border-primary/30 bg-primary/10 text-link",
  },
  interrupted: {
    label: "interrupted",
    icon: ZapOff,
    className: "border-notice/30 bg-notice/10 text-notice",
  },
};

const FALLBACK_META: JobStatusMeta = {
  label: "unknown",
  icon: CircleSlash,
  className: "bg-muted text-muted-foreground",
};

function JobStatusBadge({ status, className }: { status: string; className?: string }) {
  const meta = JOB_STATUS_META[status] ?? { ...FALLBACK_META, label: status };
  const Icon = meta.icon;

  return (
    <Badge className={cn("uppercase tracking-wider", meta.className, className)}>
      <Icon className={cn("size-3", status === "running" && "animate-spin")} />
      <span>{meta.label}</span>
    </Badge>
  );
}

export { JobStatusBadge };
