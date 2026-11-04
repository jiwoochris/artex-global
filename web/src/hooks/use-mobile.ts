import * as React from "react";

const MOBILE_BREAKPOINT = 768;
// With the sidebar expanded, a narrower body width makes tables and cards overlap (the 768~1279px range).
// Below this width the sidebar auto-collapses to icon mode to free up body width.
export const TABLET_BREAKPOINT = 1280;

function useBreakpoint(breakpoint: number) {
  const [below, setBelow] = React.useState<boolean | undefined>(undefined);

  React.useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${breakpoint - 1}px)`);
    const onChange = () => setBelow(window.innerWidth < breakpoint);
    mql.addEventListener("change", onChange);
    setBelow(window.innerWidth < breakpoint);
    return () => mql.removeEventListener("change", onChange);
  }, [breakpoint]);

  return below;
}

export function useIsMobile() {
  return !!useBreakpoint(MOBILE_BREAKPOINT);
}

// Whether the width is tablet (768~1279px). On the initial render (before measuring) it returns undefined as-is,
// so callers can tell "not known yet" apart from "false".
export function useIsTablet() {
  return useBreakpoint(TABLET_BREAKPOINT);
}
