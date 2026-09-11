import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/toast";
import { AppRouter } from "./router";

export function App() {
  return (
    <TooltipProvider>
      <AppRouter />
      <Toaster />
    </TooltipProvider>
  );
}
