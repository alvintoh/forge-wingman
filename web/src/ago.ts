/** ago words how long before `now` the ISO time or millisecond time `at` was: "just now", "14m ago", "3h ago", "2d ago". */
export function ago(at: string | number, now: number, suffix = " ago"): string {
  const since = typeof at === "number" ? at : Date.parse(at);
  const minutes = Math.max(0, Math.floor((now - since) / 60_000));
  if (Number.isNaN(minutes)) {
    return "at an unknown time";
  }
  if (minutes < 1) {
    return "just now";
  }
  if (minutes < 60) {
    return `${minutes}m${suffix}`;
  }
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h${suffix}` : `${Math.floor(hours / 24)}d${suffix}`;
}
