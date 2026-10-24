import { useCallback, useEffect, useState } from "react";

// What one of the page's reads holds: nothing yet, a failure, or the answer.
export type Read<T> = { status: "loading" } | { status: "error" } | { status: "ready"; body: T };

// useRead reads url and returns the answer once guard has checked it, so a
// read that fails, or answers something else, shows as an error and never as
// an empty answer. token names the failure in the console. reload reads it
// again and keeps the answer it holds until the new one arrives.
export function useRead<T>(
  url: string,
  guard: (body: unknown) => body is T,
  token: string,
): Read<T> & { reload: () => void } {
  const [state, setState] = useState<Read<T>>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  const reload = useCallback(() => setAttempt((count) => count + 1), []);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch(url, { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`${url} answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!guard(body)) {
        throw new Error(`${url} answered a document of another shape`);
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", body });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error(token, err);
      setState({ status: "error" });
    });

    return () => controller.abort();
  }, [url, guard, token, attempt]);

  return { ...state, reload };
}
