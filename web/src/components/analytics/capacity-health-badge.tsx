import { CheckCircle2, AlertTriangle, AlertOctagon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";

export interface CapacityHealthProps {
  avgQueueSeconds: number;
}

export function getCapacityStatus(avgQueueSeconds: number): {
  status: "optimal" | "moderate" | "constrained";
  label: string;
  description: string;
  badgeClass: string;
  dotClass: string;
} {
  if (avgQueueSeconds < 5.0) {
    return {
      status: "optimal",
      label: "Optimal Capacity",
      description:
        "Warm idle runners immediately pick up incoming workflow jobs with sub-5s latency.",
      badgeClass: "border-success/30 bg-success/10 text-success",
      dotClass: "bg-success",
    };
  }
  if (avgQueueSeconds <= 30.0) {
    return {
      status: "moderate",
      label: "Moderate Load",
      description:
        "Cold container spin-up overhead observed. Consider increasing warm idle runner targets.",
      badgeClass: "border-warning/30 bg-warning/10 text-warning",
      dotClass: "bg-warning",
    };
  }
  return {
    status: "constrained",
    label: "Capacity Constrained",
    description:
      "High queue wait latency detected (>30s). Scaling bottleneck; increase max concurrency.",
    badgeClass: "border-destructive/30 bg-destructive/10 text-destructive",
    dotClass: "bg-destructive",
  };
}

export function CapacityHealthBadge({ avgQueueSeconds }: CapacityHealthProps) {
  const info = getCapacityStatus(avgQueueSeconds);

  return (
    <Badge
      variant="outline"
      className={cn("h-auto gap-1.5 px-3 py-1 text-xs font-semibold shadow-2xs", info.badgeClass)}
      title={info.description}
    >
      <span className={`h-2 w-2 rounded-full ${info.dotClass} animate-pulse`} />
      {info.status === "optimal" ? (
        <CheckCircle2 />
      ) : info.status === "moderate" ? (
        <AlertTriangle />
      ) : (
        <AlertOctagon />
      )}
      <span>{info.label}</span>
    </Badge>
  );
}
