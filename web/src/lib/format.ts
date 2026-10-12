/** Display helpers that are not money (see lib/money for amounts). */

/** "0x1234abcd…9f0e": keeps the prefix and the tail, which people compare. */
export function shortHash(value: string, head = 10, tail = 6): string {
  if (value.length <= head + tail + 1) return value
  return `${value.slice(0, head)}…${value.slice(-tail)}`
}

/** A UUID's first block, enough to tell rows apart: "0b6f6a52". */
export function shortId(id: string): string {
  return id.split('-')[0] ?? id
}

function valid(iso: string): Date | null {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

/** RFC 3339 → "14:03:22" local time. */
export function formatTime(iso: string): string {
  const d = valid(iso)
  return d ? d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : iso
}

/** RFC 3339 → "8 Oct, 14:03:22" local time. */
export function formatDateTime(iso: string): string {
  const d = valid(iso)
  if (!d) return iso
  const date = d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
  return `${date}, ${formatTime(iso)}`
}

/** "3s ago", "5m ago", "2h ago", else the date. `now` is injectable for tests. */
export function formatAgo(iso: string, now: number = Date.now()): string {
  const d = valid(iso)
  if (!d) return iso
  const s = Math.max(0, Math.round((now - d.getTime()) / 1000))
  if (s < 60) return `${String(s)}s ago`
  if (s < 3600) return `${String(Math.floor(s / 60))}m ago`
  if (s < 86_400) return `${String(Math.floor(s / 3600))}h ago`
  return formatDateTime(iso)
}

/** Groups a CLABE as bank · plaza · account · check digit: "002 010 07777777777 1". */
export function formatClabe(clabe: string): string {
  if (!/^\d{18}$/.test(clabe)) return clabe
  return `${clabe.slice(0, 3)} ${clabe.slice(3, 6)} ${clabe.slice(6, 17)} ${clabe.slice(17)}`
}

/** "1 tick", "5 ticks". */
export function plural(n: number, one: string, many = `${one}s`): string {
  return `${String(n)} ${n === 1 ? one : many}`
}
