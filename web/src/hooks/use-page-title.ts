import { useEffect } from "react";

/**
 * Route page title (docs/36 §5.1): sets `document.title` to
 * `<title> · Runnero`. Detail pages compose the entity into `title`
 * (e.g. `` `${pool.name} · Pools` ``). While `title` is undefined
 * (entity data still loading) the previous title stays in place, so the
 * effect only ever runs with a complete suffix.
 */
export function usePageTitle(title: string | undefined) {
  useEffect(() => {
    if (title) document.title = `${title} · Runnero`;
  }, [title]);
}
