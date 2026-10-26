import { useCallback, useEffect, useState } from "react";

// What one of the page's reads holds: nothing yet, a failure, or the answer. A failure and an
// answer name the url they are for, which tells them from the last one's after the url changes.
export type Read<T> =
  | { status: "loading" }
  | { status: "error"; url: string }
  | { status: "ready"; body: T; url: string };

const LOADING = { status: "loading" } as const;

// useRead reads url and returns the answer once guard has checked it, so a
// read that fails, or answers something else, shows as an error and never as
// an empty answer. token names the failure in the console. reload reads it
// again and keeps the answer it holds until the new one arrives, as a changed
// url does; a null url reads nothing and drops the answer, so the url read next
// is read afresh.
export function useRead<T>(
  url: string | null,
  guard: (body: unknown) => body is T,
  token: string,
): Read<T> & { reload: () => void } {
  const [state, setState] = useState<Read<T>>(LOADING);
  const [attempt, setAttempt] = useState(0);
  const [lastUrl, setLastUrl] = useState(url);
  if (url !== lastUrl) {
    setLastUrl(url);
    if (url === null) {
      setState(LOADING);
    }
  }
  const reload = useCallback(() => setAttempt((count) => count + 1), []);

  useEffect(() => {
    if (url === null) {
      return;
    }
    const controller = new AbortController();

    async function read(from: string) {
      const response = await fetch(from, { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`${from} answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!guard(body)) {
        throw new Error(`${from} answered a document of another shape`);
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", body, url: from });
    }

    read(url).catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error(token, err);
      setState({ status: "error", url });
    });

    return () => controller.abort();
  }, [url, guard, token, attempt]);

  return { ...state, reload };
}
