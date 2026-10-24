export function postJSON(
  path: string,
  body: Record<string, string | number | boolean>,
): Promise<Response> {
  return fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}
