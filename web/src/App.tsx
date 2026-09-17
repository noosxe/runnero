import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/toast";
import { PermissionDeniedToast } from "@/components/common/permission-denied-toast";
import { AppRouter } from "./router";

export function App() {
  return (
    <TooltipProvider>
      <AppRouter />
      <Toaster />
      {/* PermissionDenied feedback (docs/35 section 2.4): toast, not logout. */}
      <PermissionDeniedToast />
    </TooltipProvider>
  );
}
