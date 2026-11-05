import { useLayoutEffect, useRef, useState } from "react";
import { overflows } from "./stop-detail";

// useOverflows says whether the content of the element its ref is on is taller than the element, now,
// as it resizes and whenever content changes. It is false until the element has been measured.
export function useOverflows<T extends HTMLElement>(content: unknown) {
  const ref = useRef<T>(null);
  const [overflowing, setOverflowing] = useState(false);

  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) {
      setOverflowing(false);
      return;
    }
    const measure = () => setOverflowing(overflows(element));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [content]);

  return { ref, overflowing };
}
