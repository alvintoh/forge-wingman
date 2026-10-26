import { postJSON } from "../post-json";

/**
 * saveSetting posts body to path and returns the answer once guard has checked it, so a failed
 * save, or an answer of another shape, is an error the caller reports rather than a saved value.
 */
export async function saveSetting<T>(
  path: string,
  body: Record<string, string | number | boolean>,
  guard: (answer: unknown) => answer is T,
): Promise<T> {
  const response = await postJSON(path, body);
  if (!response.ok) {
    throw new Error(`${path} answered ${response.status}`);
  }
  const answer: unknown = await response.json();
  if (!guard(answer)) {
    throw new Error(`${path} answered a document of another shape`);
  }
  return answer;
}
