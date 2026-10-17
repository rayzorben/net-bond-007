export interface Sample { t: number; r: number; j: number; l: number; tx: number; rx: number; w: number }

export interface LinkView {
  id: number; name: string; label?: string; kind: string; state: string
  backup: boolean; weight: number
  rttMs: number; minRttMs: number; jitterMs: number; lossPct: number; quality: number
  txBps: number; rxBps: number; capBps: number; paceBps: number; shapeBps: number
  benched: boolean; reliability: number
  metered?: boolean; meterShare?: number
  tested?: { downBps: number; upBps: number; pingMs: number; at: string }
  usage?: { usedBytes: number; capBytes?: number; cycleStart: string; overCap?: boolean }
  relay?: RelayInfo
  txBytes: number; rxBytes: number; txPackets: number; rxPackets: number
  wins: number; dups: number; drops: number; errors: number; winShare: number; queue: number
  local?: string; remote?: string; lastError?: string
  history?: Sample[]
}

/** The phone behind a phone-relay link (from its last announcement). */
export interface RelayInfo {
  phone: string; model?: string; carrier?: string; version?: string
  cellular: boolean; battery: number; charging: boolean
  tempC?: number; thermal: number; signal: number; seenMs: number
}

export interface EngineView {
  session: number; txBps: number; rxBps: number
  uniqueTxBytes: number; uniqueRxBytes: number; wireTxBytes: number; wireRxBytes: number
  noLinkDrops: number; reorderHoldMs: number; reorderHeld: number
  reorderInOrder: number; reorderWaited: number; reorderSkipped: number; reorderLate: number
  mode: string; redundancy: number
  arq: boolean; retransmits: number; nacksSent: number; nacksRecv: number
  gapsDetected: number; gapsRecovered: number; gapsGaveUp: number
}

export interface IfaceView {
  name: string; kind: string; desc?: string; ipv4: string[] | null; up: boolean; status: string
  linkId?: number; label?: string; backup?: boolean; weight?: number; maxMbps?: number
}

export interface ClientState {
  role: 'client'; name: string; uptime: number
  server: { address: string; resolved?: string; name?: string; version?: string; connected: boolean; enabled: boolean; error?: string }
  tunnel: { up: boolean; name?: string; ip?: string; gateway?: string; mtu?: number; routes: number; dns?: string; error?: string; bypass?: string }
  engine: EngineView
  links: LinkView[]
  interfaces: IfaceView[]
  warnings: string[]
  auto?: { mode: string; copies: number; direct: boolean; directVia?: string; benched?: string[]; guard?: { active: boolean; via?: string; bondedMbps?: number; directMbps?: number; testedAt?: number; nextAt?: number; error?: string } }
}

export interface SessionView {
  id: string; name: string; os: string; version: string; ip: string
  created: number; lastSeen: number; engine: EngineView; links: LinkView[] | null
}

export interface ServerState {
  role: 'server'; uptime: number; listen: string; gateway: string
  sessions: SessionView[]
  egress: Record<string, number>
  rejected: number; warnings: string[]
}

export type AnyState = ClientState | ServerState

export interface Field {
  key: string; label: string; help?: string; type: 'string' | 'int' | 'float' | 'bool' | 'enum'
  min?: number; max?: number; step?: number; unit?: string
  options?: { value: string; label: string }[]
  advanced?: boolean; secret?: boolean
}
export interface Section { key: string; label: string; help?: string; fields: Field[] }

export interface Info { role: 'client' | 'server'; version: string; os: string; arch: string; name: string }

export interface LogEntry { t: number; level: string; msg: string; attrs?: string }
