import { useOnboardingStatus, useIsAdmin } from "../lib/api/query-hooks";
import { ChangePasswordCard } from "../components/security/change-password-card";
import { PasskeysCard, PasskeysUnconfiguredCard } from "../components/security/passkeys-card";

/**
 * Account › Security tab (RUN-282, docs/37 §4.3): password change plus the
 * passkeys card. The passkeys card is visible to every role when WebAuthn
 * is configured; when it is not, the card renders for admins only as an
 * Empty state pointing at the required supervisor configuration — the
 * capability is never silently hidden (the old settings Security tab
 * behavior). Enrollment RPCs keep their server-side authorization; this
 * gating is presentational.
 */
export function AccountSecurityTab() {
  const isAdmin = useIsAdmin();
  // Cached from the authenticated route guard's beforeLoad fetch - no extra
  // RPC (docs/34 section 3.5).
  const { data: onboarding } = useOnboardingStatus();
  const passkeyAvailable = onboarding?.passkeyAvailable ?? false;

  return (
    <div className="flex flex-col gap-6">
      <ChangePasswordCard />
      {passkeyAvailable ? <PasskeysCard /> : isAdmin && <PasskeysUnconfiguredCard />}
    </div>
  );
}
