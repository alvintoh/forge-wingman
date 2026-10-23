import { useLayoutEffect, useRef, useState } from "react";

// useWidth gives the pixel width of the element its ref is on, now and as it
// resizes, so a chart can derive its geometry from the room it has. It is 0
// until the element has been measured.
export function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);

  useLayoutEffect(() => {
    const element = ref.current;
    if (element === null) {
      return;
    }
    setWidth(element.clientWidth);
    const observer = new ResizeObserver(() => setWidth(element.clientWidth));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  return { ref, width };
}
