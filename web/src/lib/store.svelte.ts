import type { AnyState, Info, LinkView, Section, LogEntry } from './types'

const HISTORY_CAP = 3600

export interface Point { t: number; rx: number; tx: number }

class Store {
  info = $state<Info | null>(null)
  authRequired = $state(false)
  authed = $state(true)
  state = $state<AnyState | null>(null)
  live = $state(false)
  /** Combined bonded throughput, one point per second. */
  totals = $state<Point[]>([])
  config = $state<any>(null)
  schema = $state<Section[]>([])
  page = $state<'overview' | 'connections' | 'history' | 'ports' | 'settings' | 'logs'>('overview')
  selected = $state<string | null>(null) // link key for the drill-down drawer
  toasts = $state<{ id: number; kind: 'ok' | 'err' | 'info'; text: string }[]>([])

  private es: EventSource | null = null
  private toastId = 0

  async boot() {
    const r = await fetch('/api/info').then((r) => r.json())
    this.info = r.info
    this.authRequired = r.authRequired
    this.authed = r.authenticated
    document.title = r.info.role === 'server' ? 'Net-Bond-007 · Server' : `Net-Bond-007 · ${r.info.name}`
    if (this.authed) await this.connect()
  }

  async login(password: string): Promise<string | null> {
    const r = await fetch('/api/login', { method: 'POST', body: JSON.stringify({ password }) })
    if (!r.ok) return (await r.json()).error ?? 'Login failed'
    this.authed = true
    await this.connect()
    return null
  }

  async logout() {
    await fetch('/api/logout', { method: 'POST' })
    this.es?.close()
    this.authed = false
    this.state = null
  }

  async connect() {
    await Promise.all([this.loadConfig(), this.loadSchema()])
    this.es?.close()
    const es = new EventSource('/api/events')
    this.es = es
    es.onopen = () => (this.live = true)
    es.onerror = async () => {
      this.live = false
      // A 401 closes the stream for good; re-check auth.
      if (es.readyState === EventSource.CLOSED) {
        const r = await fetch('/api/info').then((r) => r.json()).catch(() => null)
        if (r && !r.authenticated) this.authed = false
        else setTimeout(() => this.connect(), 2000)
      }
    }
    es.onmessage = (ev) => this.ingest(JSON.parse(ev.data))
  }

  private ingest(next: AnyState) {
    const prev = this.state
    if (next.role === 'client') {
      next.links = mergeLinks(prev?.role === 'client' ? prev.links : undefined, next.links ?? [])
      this.pushTotal(next.engine.rxBps, next.engine.txBps)
    } else {
      const prevSessions = new Map(prev?.role === 'server' ? prev.sessions.map((s) => [s.id, s]) : [])
      let rx = 0, tx = 0
      for (const s of next.sessions) {
        s.links = mergeLinks(prevSessions.get(s.id)?.links ?? undefined, s.links ?? [])
        rx += s.engine.txBps // server: what it sends down = client's download
        tx += s.engine.rxBps
      }
      this.pushTotal(rx, tx)
    }
    this.state = next
    this.live = true
  }

  private pushTotal(rx: number, tx: number) {
    const t = Math.floor(Date.now() / 1000) * 1000
    const last = this.totals[this.totals.length - 1]
    if (last && last.t === t) {
      last.rx = rx
      last.tx = tx
      return
    }
    this.totals.push({ t, rx, tx })
    if (this.totals.length > 600) this.totals.splice(0, this.totals.length - 600)
  }

  async loadConfig() {
    this.config = await api('GET', '/api/config')
  }

  async loadSchema() {
    this.schema = await api('GET', '/api/schema')
  }

  /**
   * Save settings. Only the fields that differ from `base` (what the caller
   * started from) are sent, so a stale tab or an old copy can never silently
   * revert settings it didn't touch. Returns an error string or null.
   */
  async saveConfig(cfg: any, base: any = $state.snapshot(this.config)): Promise<string | null> {
    const patch = diff(base, cfg)
    if (patch === undefined) return null
    try {
      this.config = await api('PUT', '/api/config', patch)
      return null
    } catch (e: any) {
      return e.message
    }
  }

  async saveLink(name: string, lc: any): Promise<string | null> {
    try {
      await api('PUT', `/api/x/links/${encodeURIComponent(name)}`, lc)
      await this.loadConfig()
      return null
    } catch (e: any) {
      return e.message
    }
  }

  async logs(): Promise<LogEntry[]> {
    return api('GET', '/api/x/logs')
  }

  toast(text: string, kind: 'ok' | 'err' | 'info' = 'ok') {
    const id = ++this.toastId
    this.toasts.push({ id, kind, text })
    setTimeout(() => (this.toasts = this.toasts.filter((t) => t.id !== id)), kind === 'err' ? 6000 : 2600)
  }
}

function mergeLinks(prev: LinkView[] | undefined, next: LinkView[]): LinkView[] {
  if (!prev) return next
  const byKey = new Map(prev.map((l) => [key(l), l]))
  for (const l of next) {
    const old = byKey.get(key(l))
    const incoming = l.history ?? []
    if (old?.history && incoming.length <= 2) {
      const h = old.history
      for (const s of incoming) if (!h.length || s.t > h[h.length - 1].t) h.push(s)
      if (h.length > HISTORY_CAP) h.splice(0, h.length - HISTORY_CAP)
      l.history = h
    }
  }
  return next
}

export const key = (l: { id: number; name: string }) => `${l.id}:${l.name}`

/** Deep difference: the parts of `next` that differ from `prev` (undefined if none). */
export function diff(prev: any, next: any): any {
  if (typeof next !== 'object' || next === null || Array.isArray(next) || typeof prev !== 'object' || prev === null || Array.isArray(prev)) {
    return JSON.stringify(prev) === JSON.stringify(next) ? undefined : next
  }
  const out: any = {}
  let any = false
  for (const k of Object.keys(next)) {
    const d = diff(prev[k], next[k])
    if (d !== undefined) { out[k] = d; any = true }
  }
  return any ? out : undefined
}

export class ApiError extends Error {
  constructor(message: string, public status: number, public data: any) { super(message) }
}

export async function api(method: string, url: string, body?: unknown, headers?: Record<string, string>) {
  const r = await fetch(url, {
    method,
    headers: body || headers ? { ...(body ? { 'Content-Type': 'application/json' } : {}), ...headers } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  const data = await r.json().catch(() => ({}))
  if (!r.ok) throw new ApiError(data.error ?? r.statusText, r.status, data)
  return data
}

export const store = new Store()

// theme
export function initTheme() {
  let t: string | null = null
  try { t = localStorage.getItem('b7-theme') } catch {}
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t
}
export function toggleTheme() {
  const cur = document.documentElement.dataset.theme
  const isDark = cur !== 'light'
  const next = isDark ? 'light' : 'dark'
  document.documentElement.dataset.theme = next
  try { localStorage.setItem('b7-theme', next) } catch {}
}
