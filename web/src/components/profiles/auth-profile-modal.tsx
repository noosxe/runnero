import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useStore } from "@tanstack/react-form";
import { create } from "@bufbuild/protobuf";
import { CreateAuthProfileRequestSchema, UpdateAuthProfileRequestSchema } from "../../gen/api_pb";
import { useCreateAuthProfile, useUpdateAuthProfile } from "../../lib/api/query-hooks";
import { FEATURE_DISABLED_HINT, isProviderGated } from "../../lib/feature-gates";
import {
  useAppForm,
  validateMessage,
  groupByField,
  violationsFromConnectError,
  applyFieldErrors,
} from "../../lib/forms";
import { fromWireAuthMethod, toWireAuthMethod } from "../../lib/utils/auth-methods";
import type { AuthProfile } from "../../gen/api_pb";
import { KeyRound, AlertCircle } from "lucide-react";

/**
 * Shared create/edit modal for Git auth profiles (docs/17 §6.2, forms on the
 * docs/30 toolkit).
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

interface ProfileFormValues {
  profileName: string;
  appId: string;
  privateKeyPem: string;
  token: string;
}

const FORM_FIELDS = ["profileName", "appId", "privateKeyPem", "token"] as const;

/** proto field → form key for the inline mapping (docs/30 §5.4). */
const PROTO_FIELD_TO_FORM: Record<string, keyof ProfileFormValues> = {
  name: "profileName",
  app_id: "appId",
  private_key: "privateKeyPem",
  token: "token",
};

/** Message-level CEL ids project onto the field that causes them. */
const CEL_RULE_TO_FORM: Record<string, keyof ProfileFormValues> = {
  "auth_profile.app_id.required": "appId",
  "auth_profile.private_key.required": "privateKeyPem",
  "auth_profile.token.required": "token",
};

export function AuthProfileModal({ mode, profile, onClose }: AuthProfileModalProps) {
  const createProfileMutation = useCreateAuthProfile();
  const updateProfileMutation = useUpdateAuthProfile();
  const isEdit = mode === "edit" && !!profile;
  const pending = createProfileMutation.isPending || updateProfileMutation.isPending;

  const [authMethod, setAuthMethod] = useState<AuthMethod>(
    isEdit ? fromWireAuthMethod(profile.authMethod) : "github_pat",
  );
  const [error, setError] = useState<string | null>(null);

  const form = useAppForm({
    defaultValues: {
      profileName: isEdit ? profile.name : "",
      appId: isEdit && profile.appId > 0n ? String(profile.appId) : "",
      privateKeyPem: "",
      token: "",
    } as ProfileFormValues,
  });
  const formValues = useStore(form.store, (s) => s.values);
  useStore(form.store, (s) => s.fieldMeta);

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
      form.setFieldValue("privateKeyPem", "");
      form.setFieldValue("token", "");
    }
    setAuthMethod(method);
  };

  const secretHelperText = () => {
    if (!isEdit) return "Encrypted at rest using AES-256 in supervisor database.";
    if (methodChanged) return "Required when changing the auth method.";
    return "Leave blank to keep the existing key/token.";
  };

  /**
   * The shared violation map (docs/30 §5.4): one protovalidate evaluation on
   * the exact Create/UpdateAuthProfileRequest the submit will send. Edit-mode
   * keep-existing secrets are projected as non-empty in the EVALUATED message
   * only — that is what the server will effectively store — so the wire CELs
   * don't false-positive while name/app_id rules still evaluate.
   */
  const runEvaluation = (): {
    fieldErrors: Partial<Record<keyof ProfileFormValues, string[]>>;
    banner: string[];
  } => {
    const fieldErrors: Partial<Record<keyof ProfileFormValues, string[]>> = {};
    const banner: string[] = [];

    // Class C: app id must parse as a positive integer before BigInt.
    const appIdTrim = formValues.appId.trim();
    if (authMethod === "github_app" && !/^\d+$/.test(appIdTrim)) {
      (fieldErrors.appId ??= []).push("GitHub App ID must be a positive integer.");
    }

    const name = formValues.profileName.trim();
    const privateKeyPem = formValues.privateKeyPem.trim();
    const token = formValues.token.trim();
    const encoder = new TextEncoder();
    const wirePrivateKey =
      authMethod === "github_app" && privateKeyPem === "" && privateKeyOptional
        ? encoder.encode("keep-existing")
        : privateKeyPem === ""
          ? new Uint8Array()
          : encoder.encode(privateKeyPem);
    const wireToken =
      authMethod !== "github_app" && token === "" && tokenOptional ? "keep-existing" : token;
    const wireAppId = /^\d+$/.test(appIdTrim) ? BigInt(appIdTrim) : 0n;

    const schema = isEdit ? UpdateAuthProfileRequestSchema : CreateAuthProfileRequestSchema;
    const message = isEdit
      ? create(schema, {
          id: profile!.id,
          name,
          authMethod: toWireAuthMethod(authMethod),
          appId: wireAppId,
          privateKey: wirePrivateKey,
          token: wireToken,
        })
      : create(schema, {
          name,
          authMethod: toWireAuthMethod(authMethod),
          appId: wireAppId,
          privateKey: wirePrivateKey,
          token: wireToken,
        });
    const { byField, messageLevel } = groupByField(validateMessage(schema, message));
    for (const [protoField, messages] of byField) {
      const formKey = PROTO_FIELD_TO_FORM[protoField];
      if (formKey) {
        (fieldErrors[formKey] ??= []).push(...messages);
        continue;
      }
      banner.push(...messages);
    }
    for (const violation of messageLevel) {
      const formKey = CEL_RULE_TO_FORM[violation.ruleId];
      if (formKey) {
        (fieldErrors[formKey] ??= []).push(violation.message);
      } else {
        banner.push(violation.message);
      }
    }

    applyFieldErrors(form, fieldErrors, FORM_FIELDS);
    return { fieldErrors, banner };
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    const { fieldErrors, banner } = runEvaluation();
    if (Object.keys(fieldErrors).length > 0 || banner.length > 0) {
      setError(banner.length > 0 ? banner.join("; ") : null);
      return;
    }

    const encoder = new TextEncoder();
    const name = formValues.profileName.trim();
    const appIdTrim = formValues.appId.trim();
    const privateKeyPem = formValues.privateKeyPem.trim();
    const token = formValues.token.trim();
    try {
      if (authMethod === "github_app") {
        if (isEdit) {
          await updateProfileMutation.mutateAsync({
            id: profile!.id,
            name,
            authMethod: toWireAuthMethod(authMethod),
            appId: BigInt(appIdTrim),
            privateKey: privateKeyPem ? encoder.encode(privateKeyPem) : new Uint8Array(),
            token: "",
          });
        } else {
          await createProfileMutation.mutateAsync({
            name,
            authMethod: toWireAuthMethod(authMethod),
            appId: BigInt(appIdTrim),
            privateKey: encoder.encode(privateKeyPem),
            token: "",
          });
        }
      } else {
        if (isEdit) {
          await updateProfileMutation.mutateAsync({
            id: profile!.id,
            name,
            authMethod: toWireAuthMethod(authMethod),
            appId: 0n,
            privateKey: new Uint8Array(),
            token,
          });
        } else {
          await createProfileMutation.mutateAsync({
            name,
            authMethod: toWireAuthMethod(authMethod),
            appId: 0n,
            privateKey: new Uint8Array(),
            token,
          });
        }
      }
      onClose();
    } catch (err: unknown) {
      // Server is the authority (docs/30 §5.4): typed violations re-enter the
      // inline mapping; anything unmapped keeps the banner fallback.
      const violations = violationsFromConnectError(err);
      if (!violations) {
        setError(err instanceof Error ? err.message : "Failed to save authentication profile");
        return;
      }
      const { byField, messageLevel } = groupByField(violations);
      const serverFieldErrors: Partial<Record<keyof ProfileFormValues, string[]>> = {};
      const bannerMessages = messageLevel.map((violation) => violation.message);
      for (const [protoField, messages] of byField) {
        const formKey = PROTO_FIELD_TO_FORM[protoField];
        if (formKey) {
          serverFieldErrors[formKey] = messages;
        } else {
          bannerMessages.push(...messages);
        }
      }
      applyFieldErrors(form, serverFieldErrors, FORM_FIELDS);
      setError(
        bannerMessages.length > 0 || Object.keys(serverFieldErrors).length > 0
          ? [bannerMessages, ...Object.values(serverFieldErrors)].flat().join("; ")
          : "Failed to save authentication profile",
      );
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="gap-4 p-6 text-xs sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <KeyRound className="size-5 text-link" />
            {isEdit ? "Edit Git Auth Profile" : "Add Git Auth Profile"}
          </DialogTitle>
        </DialogHeader>

        {error && (
          <div
            role="alert"
            className="flex items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 p-3 text-destructive"
          >
            <AlertCircle className="size-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} noValidate className="grid gap-4">
          <div>
            <div className="text-xs font-medium">Provider Method</div>
            <ToggleGroup
              variant="outline"
              className="mt-2 grid w-full grid-cols-2 gap-2 sm:grid-cols-4"
              value={[authMethod]}
              onValueChange={(value) => {
                if (value[0]) handleMethodChange(value[0] as AuthMethod);
              }}
            >
              {AUTH_METHODS.map((m) => (
                <ToggleGroupItem
                  key={m.id}
                  value={m.id}
                  disabled={isProviderGated(m.id)}
                  title={
                    isProviderGated(m.id) ? `${m.label} — ${FEATURE_DISABLED_HINT}` : undefined
                  }
                  className="h-auto w-full py-2 text-center"
                >
                  {m.label}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>

          <form.AppField name="profileName">
            {(field) => (
              <field.TextField
                label="Profile Name"
                id="modal-profile-name"
                placeholder="e.g. github-production"
                onBlurExtra={runEvaluation}
              />
            )}
          </form.AppField>

          {authMethod === "github_app" ? (
            <>
              <form.AppField name="appId">
                {(field) => (
                  <field.TextField
                    label="GitHub App ID"
                    id="modal-app-id"
                    type="number"
                    placeholder="e.g. 123456"
                    onBlurExtra={runEvaluation}
                  />
                )}
              </form.AppField>

              <form.AppField name="privateKeyPem">
                {(field) => (
                  <field.TextareaField
                    label="Private Key (.pem)"
                    id="modal-private-key"
                    rows={4}
                    inputClassName="font-mono text-[11px]"
                    placeholder="-----BEGIN RSA PRIVATE KEY-----"
                    description={secretHelperText()}
                    onBlurExtra={runEvaluation}
                  />
                )}
              </form.AppField>
            </>
          ) : (
            <form.AppField name="token">
              {(field) => (
                <field.PasswordField
                  label="Personal Access Token (PAT)"
                  id="modal-token"
                  placeholder="ghp_... or gitea_pat_..."
                  description={secretHelperText()}
                  onBlurExtra={runEvaluation}
                />
              )}
            </form.AppField>
          )}

          <div className="flex justify-end gap-2 border-t border-border pt-3">
            <Button variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" onMouseDown={(e) => e.preventDefault()} disabled={pending}>
              {pending ? "Saving..." : "Save Profile"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
