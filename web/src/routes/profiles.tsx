import { useState } from "react";
import { Button } from "@/components/ui/button";
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
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
            Git Auth Profiles
          </h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">
            Credentials for requesting ephemeral runner registration tokens from Git providers.
          </p>
        </div>

        <Button onClick={() => setModal({ mode: "create" })}>
          <Plus data-icon="inline-start" />
          <span>Add Auth Profile</span>
        </Button>
      </div>

      {isLoading ? (
        <div className="text-sm text-slate-400">Loading auth profiles...</div>
      ) : !profiles || profiles.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-slate-300 p-12 text-center text-slate-500 dark:border-slate-800 dark:text-slate-400">
          <KeyRound className="mx-auto h-8 w-8 text-slate-400 mb-2" />
          <p className="text-base font-semibold text-slate-800 dark:text-slate-200">
            No auth profiles configured
          </p>
          <p className="text-xs text-slate-500 mt-1 max-w-md mx-auto">
            Connect a GitHub App or Personal Access Token (PAT) for GitHub, Gitea, or Forgejo to
            begin orchestrating runner pools.
          </p>
          <div className="mt-4">
            <Button size="xs" onClick={() => setModal({ mode: "create" })}>
              <Plus data-icon="inline-start" />
              <span>Add First Profile</span>
            </Button>
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          {profiles.map((prof) => (
            <div
              key={prof.id.toString()}
              className="rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900 flex flex-col justify-between"
            >
              <div>
                <div className="flex items-center justify-between">
                  <span className="font-bold text-slate-900 dark:text-white">{prof.name}</span>
                  <span className="rounded-md bg-slate-100 px-2.5 py-1 text-xs font-semibold uppercase text-slate-700 dark:bg-slate-800 dark:text-slate-300">
                    {prof.authMethod}
                  </span>
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-slate-500">
                  <div className="flex items-center gap-1.5">
                    <ShieldCheck className="h-4 w-4 text-emerald-500" />
                    <span>Encrypted AES-256 (Write-Only)</span>
                  </div>

                  {(prof.hasPrivateKey || prof.hasToken) && (
                    <span className="rounded-md border border-slate-200 bg-slate-50 px-2 py-0.5 text-[11px] font-medium text-slate-600 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300">
                      {prof.hasPrivateKey ? "Private Key: Configured" : "Token: Configured"}
                    </span>
                  )}

                  {prof.authMethod === "github_app" &&
                    (prof.installationsCount > 0 ? (
                      <span className="inline-flex items-center gap-1 rounded-md bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-700 border border-emerald-200 dark:bg-emerald-950/40 dark:text-emerald-300 dark:border-emerald-800">
                        <CheckCircle2 className="h-3 w-3" />
                        <span>
                          Installed on {prof.installationsCount}{" "}
                          {prof.installationsCount === 1 ? "account" : "accounts"}
                        </span>
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 rounded-md bg-amber-50 px-2 py-0.5 text-[11px] font-medium text-amber-700 border border-amber-200 dark:bg-amber-950/40 dark:text-amber-300 dark:border-amber-800">
                        <AlertCircle className="h-3 w-3" />
                        <span>Not Installed</span>
                      </span>
                    ))}
                </div>
              </div>

              <div className="mt-5 flex items-center justify-between border-t border-slate-100 pt-3 dark:border-slate-800">
                <div>
                  {prof.authMethod === "github_app" && prof.installUrl && (
                    <a
                      href={prof.installUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      className={`inline-flex items-center gap-1 text-xs font-semibold ${
                        prof.installationsCount === 0
                          ? "text-blue-600 hover:text-blue-700 dark:text-blue-400 dark:hover:text-blue-300"
                          : "text-slate-600 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200"
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
            </div>
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
