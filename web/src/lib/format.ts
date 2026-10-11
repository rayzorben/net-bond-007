export function bps(v: number, digits = 1): string {
  if (!isFinite(v) || v <= 0) return '0 bps'
  const u = ['bps', 'Kbps', 'Mbps', 'Gbps']
  let i = 0
  while (v >= 1000 && i < u.length - 1) { v /= 1000; i++ }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : digits)} ${u[i]}`
}

export function bpsParts(v: number): [string, string] {
  const s = bps(v)
  const i = s.lastIndexOf(' ')
  return [s.slice(0, i), s.slice(i + 1)]
}

export function bytes(v: number): string {
  if (!isFinite(v) || v <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${u[i]}`
}

export function ms(v: number): string {
  if (!v) return '—'
  return v >= 100 ? `${v.toFixed(0)} ms` : `${v.toFixed(1)} ms`
}

export function pct(v: number, digits = 1): string {
  return `${(v ?? 0).toFixed(digits)}%`
}

export function duration(sec: number): string {
  sec = Math.floor(sec)
  const d = Math.floor(sec / 86400), h = Math.floor(sec / 3600) % 24, m = Math.floor(sec / 60) % 60, s = sec % 60
  if (d) return `${d}d ${h}h`
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${s}s`
  return `${s}s`
}

export function count(v: number): string {
  if (v >= 1e9) return (v / 1e9).toFixed(1) + 'B'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e4) return (v / 1e3).toFixed(0) + 'K'
  return String(v ?? 0)
}

export const kindLabel: Record<string, string> = {
  ethernet: 'Ethernet', wifi: 'Wi-Fi', cellular: 'Cellular', usb: 'USB tether',
  bluetooth: 'Bluetooth', vpn: 'VPN', virtual: 'Virtual', other: 'Network',
}

export function kindColor(kind: string): string {
  const k = ['ethernet', 'wifi', 'cellular', 'usb', 'bluetooth'].includes(kind) ? kind : 'other'
  return `var(--k-${k})`
}

/** Resolve a CSS variable to a concrete colour (for canvas drawing). */
export function cssColor(v: string, el: Element = document.documentElement): string {
  const m = /^var\((--[\w-]+)\)$/.exec(v)
  if (!m) return v
  return getComputedStyle(el).getPropertyValue(m[1]).trim() || '#888'
}

export function stateTone(state: string): 'ok' | 'warn' | 'bad' | 'info' | '' {
  switch (state) {
    case 'up': return 'ok'
    case 'degraded': return 'warn'
    case 'down': return 'bad'
    case 'connecting': return 'info'
  }
  return ''
}

export function displayName(l: { name: string; label?: string }): string {
  return l.label || l.name
}
