// The zone is fixed, not the viewer's, so one instant reads the same on every machine; the zone
// database carries daylight saving.
const SYDNEY = new Intl.DateTimeFormat("en-US", {
  timeZone: "Australia/Sydney",
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
});

// English names from a table: en-AU and en-GB print September as "Sept".
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function partOf(parts: Intl.DateTimeFormatPart[], type: Intl.DateTimeFormatPartTypes): string {
  return parts.find((part) => part.type === type)?.value ?? "";
}

/**
 * sydneyDateTime is an instant as Sydney's clock reads it, in 24 hours with no year or zone:
 * "2026-10-17T10:20:00Z" is "17 Oct, 21:20". It is null for a text that is no instant.
 */
export function sydneyDateTime(instant: string): string | null {
  const time = Date.parse(instant);
  if (Number.isNaN(time)) {
    return null;
  }
  const parts = SYDNEY.formatToParts(time);
  const month = MONTHS[Number(partOf(parts, "month")) - 1];
  return `${partOf(parts, "day")} ${month}, ${partOf(parts, "hour")}:${partOf(parts, "minute")}`;
}
