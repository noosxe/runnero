import { useState, type FormEvent } from "react";
import { FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { cn } from "cn";
import { useCreateAuthProfile, useUpdateAuthProfile } from "../../lib/api/query-hooks";
import { fromWireAuthMethod, toWireAuthMethod } from "../../lib/utils/auth-methods";
import type { AuthProfile } from "../../gen/api_pb";
import { KeyRound, X, AlertCircle } from "lucide-react";

/**
 * Shared create/edit modal for Git auth profiles (docs/17 §6.2).
 *
 * Create mode starts from an empty form. Edit mode prefills name, auth method,
 * and App ID, but secret inputs always start empty: the write-only secret model
 * means the raw key/token is never readable, so a blank secret field is
 * transmitted as empty and the server interprets it as "keep the existing
 * encrypted secret". Switching the auth method clears secret inputs and marks
 * them required, mirroring the server's no-keep-across-switch rule.
 */
export interface AuthProfileModalProps {
  mode: "create" | "edit";
  /** Profile to edit; required in edit mode, ignored in create mode. */
  profile?: AuthProfile;
  onClose: () => void;
}

type AuthMethod = "github_app" | "github_pat" | "gitea_pat" | "forgejo_pat";

const AUTH_METHODS: { id: AuthMethod; label: string }[] = [
  { id: "github_pat", label: "GitHub PAT" },
  { id: "github_app", label: "GitHub App" },
  { id: "gitea_pat", label: "Gitea PAT" },
  { id: "forgejo_pat", label: "Forgejo PAT" },
];

export function AuthProfileModal({ mode, profile, onClose }: AuthProfileModalProps) {
  const createProfileMutation = useCreateAuthProfile();
  const updateProfileMutation = useUpdateAuthProfile();
  const isEdit = mode === "edit" && !!profile;
  const pending = createProfileMutation.isPending || updateProfileMutation.isPending;

  const [profileName, setProfileName] = useState(isEdit ? profile.name : "");
  const [authMethod, setAuthMethod] = useState<AuthMethod>(
    isEdit ? fromWireAuthMethod(profile.authMethod) : "github_pat",
  );
  const [appId, setAppId] = useState(isEdit && profile.appId > 0n ? String(profile.appId) : "");
  const [privateKeyPem, setPrivateKeyPem] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);

  // In edit mode, secrets may be omitted only when the method is unchanged and
  // the profile actually stores a secret of that kind (docs/17 §4.2/§4.3).
  const methodChanged = isEdit && authMethod !== profile.authMethod;
  const privateKeyOptional =
    isEdit && !methodChanged && authMethod === "github_app" && profile.hasPrivateKey;
  const tokenOptional = isEdit && !methodChanged && authMethod !== "github_app" && profile.hasToken;

  const handleMethodChange = (method: AuthMethod) => {
    if (method !== authMethod) {
      // The stored secret belongs to the old method and cannot be kept across
      // a switch — clear inputs so stale text is never silently submitted.
      setPrivateKeyPem("");
      setToken("");
    }
    setAuthMethod(method);
  };

  const secretHelperText = () => {
    if (!isEdit) return "Encrypted at rest using AES-256 in supervisor database.";
    if (methodChanged) return "Required when changing the auth method.";
    return "Leave blank to keep the existing key/token.";
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!profileName.trim()) {
      setError("Profile name is required");
      return;
    }

    const encoder = new TextEncoder();
    try {
      if (authMethod === "github_app") {
        if (!appId.trim() || BigInt(appId.trim()) <= 0n) {
          setError("GitHub App ID is required");
          return;
        }
        if (!privateKeyOptional && !privateKeyPem.trim()) {
          setError("Private Key PEM is required");
          return;
        }
        if (isEdit) {
          await updateProfileMutation.mutateAsync({
            id: profile.id,
            name: profileName.trim(),
            authMethod: toWireAuthMethod(authMethod),
            appId: BigInt(appId.trim()),
            privateKey: privateKeyPem.trim()
              ? encoder.encode(privateKeyPem.trim())
              : new Uint8Array(),
            token: "",
          });
        } else {
          await createProfileMutation.mutateAsync({
            name: profileName.trim(),
            authMethod: toWireAuthMethod(authMethod),
            appId: BigInt(appId.trim()),
            privateKey: encoder.encode(privateKeyPem.trim()),
            token: "",
          });
        }
      } else {
        if (!tokenOptional && !token.trim()) {
          setError("Personal Access Token (PAT) is required");
          return;
        }
        if (isEdit) {
          await updateProfileMutation.mutateAsync({
            id: profile.id,
            name: profileName.trim(),
            authMethod: toWireAuthMethod(authMethod),
            appId: 0n,
            privateKey: new Uint8Array(),
            token: token.trim(),
          });
        } else {
          await createProfileMutation.mutateAsync({
            name: profileName.trim(),
            authMethod: toWireAuthMethod(authMethod),
            appId: 0n,
            privateKey: new Uint8Array(),
            token: token.trim(),
          });
        }
      }
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save authentication profile");
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/60 backdrop-blur-xs p-4">
      <div className="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 text-xs">
        <div className="flex items-center justify-between border-b border-slate-100 pb-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <KeyRound className="h-5 w-5 text-blue-600 dark:text-blue-400" />
            <h3 className="text-base font-bold text-slate-900 dark:text-white">
              {isEdit ? "Edit Git Auth Profile" : "Add Git Auth Profile"}
            </h3>
          </div>
          <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close">
            <X />
          </Button>
        </div>

        {error && (
          <div
            role="alert"
            className="mt-3 flex items-center gap-2 rounded-xl border border-rose-200 bg-rose-50/80 p-3 text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/30 dark:text-rose-300"
          >
            <AlertCircle className="h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          <div>
            <FieldLabel className="dark:">Provider Method</FieldLabel>
            <div className="mt-2 grid grid-cols-2 gap-2 sm:grid-cols-4">
              {AUTH_METHODS.map((m) => (
                <Button
                  key={m.id}
                  type="button"
                  variant="outline"
                  aria-pressed={authMethod === m.id}
                  onClick={() => handleMethodChange(m.id)}
                  className={cn(
                    "h-auto w-full py-2 text-center",
                    authMethod === m.id && "border-primary bg-primary/5 text-primary font-semibold",
                  )}
                >
                  {m.label}
                </Button>
              ))}
            </div>
          </div>

          <div>
            <FieldLabel htmlFor="modal-profile-name" className="dark:">
              Profile Name
            </FieldLabel>
            <Input
              id="modal-profile-name"
              type="text"
              placeholder="e.g. github-production"
              value={profileName}
              onChange={(e) => setProfileName(e.target.value)}

              required
            />
          </div>

          {authMethod === "github_app" ? (
            <>
              <div>
                <FieldLabel htmlFor="modal-app-id" className="dark:">
                  GitHub App ID
                </FieldLabel>
                <Input
                  id="modal-app-id"
                  type="number"
                  placeholder="e.g. 123456"
                  value={appId}
                  onChange={(e) => setAppId(e.target.value)}

                  required
                />
              </div>

              <div>
                <FieldLabel htmlFor="modal-private-key" className="dark:">
                  Private Key (.pem)
                </FieldLabel>
                <Textarea
                  id="modal-private-key"
                  rows={4}
                  placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;...&#10;-----END RSA PRIVATE KEY-----"
                  value={privateKeyPem}
                  onChange={(e) => setPrivateKeyPem(e.target.value)}
                  aria-required={!privateKeyOptional}
                  className="font-mono text-[11px]"
                  required={!privateKeyOptional}
                />
                <p className="mt-1 text-[11px] text-slate-400">{secretHelperText()}</p>
              </div>
            </>
          ) : (
            <div>
              <FieldLabel htmlFor="modal-token" className="dark:">
                Personal Access Token (PAT)
              </FieldLabel>
              <Input
                id="modal-token"
                type="password"
                placeholder="ghp_... or gitea_pat_..."
                value={token}
                onChange={(e) => setToken(e.target.value)}
                aria-required={!tokenOptional}

                required={!tokenOptional}
              />
              <p className="mt-1 text-[11px] text-slate-400">{secretHelperText()}</p>
            </div>
          )}

          <div className="flex justify-end gap-2 pt-2 border-t border-slate-100 dark:border-slate-800">
            <Button variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Saving..." : "Save Profile"}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
