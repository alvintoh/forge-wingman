/** ago words how long before `now` the ISO time `iso` was: "just now", "14m ago", "3h ago", "2d ago". */
export function ago(iso: string, now: number, suffix = " ago"): string {
  const minutes = Math.max(0, Math.floor((now - Date.parse(iso)) / 60_000));
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
