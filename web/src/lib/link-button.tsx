import {
  Link,
  type AnyRouter,
  type CreateLinkProps,
  type LinkComponentProps,
  type RegisteredRouter,
} from "@tanstack/react-router";
import type { Ref } from "react";
import { Button } from "@/components/ui/button";

/**
 * Router-aware Button: a TanStack `Link` that renders as a shadcn Button.
 *
 * Composed via Base UI's `render` prop so the DOM is a real `<a>` (valid HTML,
 * native link semantics — middle-click, ⌘/Ctrl-click, "Open in new tab", and
 * copy-link-address all work) carrying the Button styles.
 * `nativeButton={false}` is required because the rendered element is not a
 * `<button>`. Accepts both router props (`to`, `params`, …) and Button props
 * (`variant`, `size`, …). See
 * https://tanstack.com/router/latest/docs/framework/react/guide/custom-link
 */
export function LinkButton<
  TRouter extends AnyRouter = RegisteredRouter,
  const TFrom extends string = string,
  const TTo extends string | undefined = undefined,
  const TMaskFrom extends string = TFrom,
  const TMaskTo extends string = "",
>(
  props: LinkComponentProps<"a", TRouter, TFrom, TTo, TMaskFrom, TMaskTo> &
    Pick<
      LinkComponentProps<typeof Button, TRouter, TFrom, TTo, TMaskFrom, TMaskTo>,
      "variant" | "size" | "focusableWhenDisabled"
    >,
) {
  // `className` routes through Button (not Link) so it merges with tailwind-merge:
  // variant color overrides like `bg-warning` cleanly replace the variant's
  // `bg-primary` instead of colliding with it in the stylesheet.
  const { variant, size, focusableWhenDisabled, className, ...linkProps } = props;
  // TanStack's conditional link-prop types can't flow through a generic body,
  // so loosen to the concrete shape `createLink` itself hands a host component.
  // `ref` is tracked separately: the element type changes to an anchor, so the
  // ref does too.
  const anchorProps = linkProps as CreateLinkProps & { ref?: Ref<HTMLAnchorElement> };
  return (
    <Button
      variant={variant}
      size={size}
      focusableWhenDisabled={focusableWhenDisabled}
      className={className}
      nativeButton={false}
      render={<Link {...anchorProps} />}
    />
  );
}
