import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuthProfiles, useDeleteAuthProfile } from "../lib/api/query-hooks";
import type { AuthProfile } from "../gen/api_pb";
import { AuthProfileModal } from "../components/profiles/auth-profile-modal";
import {
  KeyRound,
  ShieldCheck,
  Plus,
  Trash2,
  Pencil,
  AlertCircle,
  ExternalLink,
  CheckCircle2,
} from "lucide-react";

interface ProfileModalState {
  mode: "create" | "edit";
  profile?: AuthProfile;
}

export function ProfilesPage() {
  const { data: profiles, isLoading } = useAuthProfiles();
  const deleteProfileMutation = useDeleteAuthProfile();

  const [modal, setModal] = useState<ProfileModalState | null>(null);

  const handleDelete = async (id: bigint, name: string) => {
    if (confirm(`Are you sure you want to delete authentication profile "${name}"?`)) {
      try {
        await deleteProfileMutation.mutateAsync(id);
      } catch (err: unknown) {
        alert(err instanceof Error ? err.message : "Failed to delete authentication profile");
      }
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground ">Git Auth Profiles</h1>
          <p className="text-sm text-muted-foreground ">
            Credentials for requesting ephemeral runner registration tokens from Git providers.
          </p>
        </div>

        <Button onClick={() => setModal({ mode: "create" })}>
          <Plus data-icon="inline-start" />
          <span>Add Auth Profile</span>
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : !profiles || profiles.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <KeyRound />
            </EmptyMedia>
            <EmptyTitle>No auth profiles configured</EmptyTitle>
            <EmptyDescription>
              Connect a GitHub App or Personal Access Token (PAT) for GitHub, Gitea, or Forgejo to
              begin orchestrating runner pools.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button size="xs" onClick={() => setModal({ mode: "create" })}>
              <Plus data-icon="inline-start" />
              <span>Add First Profile</span>
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          {profiles.map((prof) => (
            <Card key={prof.id.toString()} size="sm" className="flex flex-col justify-between p-4">
              <div>
                <div className="flex items-center justify-between">
                  <span className="font-bold text-foreground">{prof.name}</span>
                  <span className="rounded-md bg-muted/50 px-2.5 py-1 text-xs font-semibold uppercase text-foreground/80">
                    {prof.authMethod}
                  </span>
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <div className="flex items-center gap-1.5">
                    <ShieldCheck className="h-4 w-4 text-success" />
                    <span>Encrypted AES-256 (Write-Only)</span>
                  </div>

                  {(prof.hasPrivateKey || prof.hasToken) && (
                    <span className="rounded-md border border-border/60 bg-muted/50 px-2 py-0.5 text-[11px] font-medium text-foreground/80">
                      {prof.hasPrivateKey ? "Private Key: Configured" : "Token: Configured"}
                    </span>
                  )}

                  {prof.authMethod === "github_app" &&
                    (prof.installationsCount > 0 ? (
                      <span className="inline-flex items-center gap-1 rounded-md border border-success/30 bg-success/10 px-2 py-0.5 text-[11px] font-medium text-success">
                        <CheckCircle2 className="h-3 w-3" />
                        <span>
                          Installed on {prof.installationsCount}{" "}
                          {prof.installationsCount === 1 ? "account" : "accounts"}
                        </span>
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 rounded-md border border-warning/30 bg-warning/10 px-2 py-0.5 text-[11px] font-medium text-warning">
                        <AlertCircle className="h-3 w-3" />
                        <span>Not Installed</span>
                      </span>
                    ))}
                </div>
              </div>

              <div className="mt-5 flex items-center justify-between border-t border-border/60 pt-3">
                <div>
                  {prof.authMethod === "github_app" && prof.installUrl && (
                    <a
                      href={prof.installUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      className={`inline-flex items-center gap-1 text-xs font-semibold ${
                        prof.installationsCount === 0
                          ? "text-primary hover:text-primary/80"
                          : "text-muted-foreground hover:text-foreground"
                      }`}
                    >
                      <ExternalLink className="h-3.5 w-3.5" />
                      <span>
                        {prof.installationsCount === 0 ? "Install App" : "Configure Access"}
                      </span>
                    </a>
                  )}
                </div>

                <div className="flex items-center gap-3">
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => setModal({ mode: "edit", profile: prof })}
                  >
                    <Pencil data-icon="inline-start" />
                    <span>Edit Profile</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => handleDelete(prof.id, prof.name)}
                    disabled={deleteProfileMutation.isPending}
                    className="text-destructive hover:text-destructive"
                  >
                    <Trash2 data-icon="inline-start" />
                    <span>Delete Profile</span>
                  </Button>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}

      {/* Shared Create/Edit Auth Profile Modal (docs/17 §6.2) */}
      {modal && (
        <AuthProfileModal
          mode={modal.mode}
          profile={modal.profile}
          onClose={() => setModal(null)}
        />
      )}
    </div>
  );
}
