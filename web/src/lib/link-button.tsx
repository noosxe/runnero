import { createLink } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";

/**
 * Router-aware Button: a TanStack `Link` that renders as a shadcn Button.
 * Accepts both router props (`to`, `params`, …) and Button props
 * (`variant`, `size`, …). See
 * https://tanstack.com/router/latest/docs/framework/react/guide/custom-link
 */
export const LinkButton = createLink(Button);
