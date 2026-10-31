/**
 * quietly runs work with the console captured and answers its exit code and the
 * lines it printed, so a passing test does not fill the reporter.
 */
export async function quietly(
  work: () => Promise<number>,
): Promise<{ code: number; lines: string[] }> {
  const log = console.log;
  const error = console.error;
  const lines: string[] = [];
  console.log = (...parts: unknown[]) => lines.push(parts.join(" "));
  console.error = (...parts: unknown[]) => lines.push(parts.join(" "));
  try {
    return { code: await work(), lines };
  } finally {
    console.log = log;
    console.error = error;
  }
}
