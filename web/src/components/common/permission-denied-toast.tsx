import { useEffect } from "react";
import { toast } from "@/components/ui/toast";
import { PERMISSION_DENIED_EVENT } from "@/lib/api/transport";

/**
 * Global PermissionDenied feedback (RUN-236, docs/35 §2.4): the transport
 * interceptor dispatches the event on every admin-bucket denial; this
 * listener renders a toast and keeps the user logged in - the opposite of
 * the Unauthenticated path, which routes to login. This is the
 * mid-session-demotion UX: no destructive logout, the next navigation
 * simply shows the viewer surface.
 */
export function PermissionDeniedToast() {
  useEffect(() => {
    let last = 0;
    const onDenied = () => {
      // Throttle: a viewer page can fire several denied fetches at once.
      const now = Date.now();
      if (now - last < 1500) return;
      last = now;
      toast.add({
        title: "Admin role required",
        description: "Your account has read-only access to this area.",
      });
    };
    window.addEventListener(PERMISSION_DENIED_EVENT, onDenied);
    return () => window.removeEventListener(PERMISSION_DENIED_EVENT, onDenied);
  }, []);
  return null;
}
