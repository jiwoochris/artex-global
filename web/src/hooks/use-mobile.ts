import * as React from "react";

const MOBILE_BREAKPOINT = 768;
// 사이드바가 펼쳐진 채 본문 폭이 좁아지면 표·카드가 겹친다(768~1279px 구간).
// 이 너비 미만에서는 사이드바를 아이콘 모드로 자동 접어 본문 폭을 확보한다.
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

// 태블릿 폭(768~1279px) 여부. 초기 렌더(측정 전)에는 undefined 를 그대로 돌려줘,
// 호출부가 "아직 모름"과 "false"를 구분할 수 있게 한다.
export function useIsTablet() {
  return useBreakpoint(TABLET_BREAKPOINT);
}
