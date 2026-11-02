import { useLayoutEffect, useRef, useState } from "react";

// useWrapped says whether the last child of the element its ref is on has dropped below the first,
// now and as it resizes. It is false until the element has been measured.
export function useWrapped<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [wrapped, setWrapped] = useState(false);

  useLayoutEffect(() => {
    const element = ref.current;
    const first = element?.firstElementChild;
    const last = element?.lastElementChild;
    if (!element || !(first instanceof HTMLElement) || !(last instanceof HTMLElement)) {
      return;
    }
    const measure = () => setWrapped(last.offsetTop >= first.offsetTop + first.offsetHeight);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  return { ref, wrapped };
}
