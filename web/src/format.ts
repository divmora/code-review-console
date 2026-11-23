export function ago(t?: string | null): string {
  if (!t || t.startsWith("0001-")) return "never";
  const s = Math.round((Date.now() - new Date(t).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

export function fmtNum(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e4) return Math.round(n / 1e3) + "k";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
  return String(n);
}

export function fmtDur(ms: number): string {
  if (!ms) return "—";
  const s = Math.round(ms / 1000);
  return s < 60 ? s + "s" : Math.floor(s / 60) + "m " + (s % 60) + "s";
}

// Dates are shown the same way on every page, day first with the month as
// a word so no reader takes 03/04 for 4 March: "23 Nov 2026", and with
// the time "23 Nov 2026, 09:33 IST" (the browser's time zone, named).
const dateFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric" });
const timeFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false });
const secFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });

function zoneName(d: Date): string {
  const part = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(d).find((p) => p.type === "timeZoneName");
  return part ? part.value : "";
}

// fmtDate is "23 Nov 2026".
export function fmtDate(t: string | Date): string {
  const d = typeof t === "string" ? new Date(t) : t;
  return isNaN(d.getTime()) ? "" : dateFmt.format(d);
}

// fmtDayUTC is the UTC calendar day of t ("2026-11-23" or a timestamp),
// for ranges labelled UTC: "23 Nov 2026" whatever the browser's zone.
const dayUTCFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
const monthUTCFmt = new Intl.DateTimeFormat("en-GB", { month: "short", year: "numeric", timeZone: "UTC" });
export function fmtDayUTC(t: string): string {
  const d = new Date(t);
  return isNaN(d.getTime()) ? "" : dayUTCFmt.format(d);
}

// fmtMonthUTC is "Nov 2026" (UTC).
export function fmtMonthUTC(t: string): string {
  const d = new Date(t);
  return isNaN(d.getTime()) ? "" : monthUTCFmt.format(d);
}

// fmtDateTime is "23 Nov 2026, 09:33 IST" (with seconds: 09:33:38).
export function fmtDateTime(t: string | Date, seconds = false): string {
  const d = typeof t === "string" ? new Date(t) : t;
  if (isNaN(d.getTime())) return "";
  return dateFmt.format(d) + ", " + (seconds ? secFmt : timeFmt).format(d) + " " + zoneName(d);
}

// utcTitle is the UTC time, for a hover title: "2026-11-23 04:03:38 UTC".
export function utcTitle(t: string | Date): string {
  const d = typeof t === "string" ? new Date(t) : t;
  return isNaN(d.getTime()) ? "" : d.toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC");
}

export function localTime(t: string): string {
  return fmtDateTime(t);
}
