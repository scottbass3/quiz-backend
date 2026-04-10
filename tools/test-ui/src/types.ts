export interface WsEvent {
  type: string
  payload?: Record<string, unknown>
}

export interface EventLogEntry {
  id: string
  ts: string
  event: WsEvent
}

export interface Option {
  id: string
  text: string
}

export interface ActiveQuestion {
  question_id: string
  index: number
  total: number
  text: string
  options: Option[]
}

export interface HttpLogEntry {
  id: string
  ts: string
  method: string
  url: string
  body?: unknown
  response?: unknown
  error?: string
  durationMs: number
}

export type WsStatus = 'disconnected' | 'connecting' | 'connected' | 'error'

export interface PlayerState {
  slotId: string        // stable UI key
  playerName: string
  playerId: string
  joined: boolean
  wsStatus: WsStatus
  ws: WebSocket | null
  events: EventLogEntry[]
  activeQuestion: ActiveQuestion | null
  answeredQuestionId: string | null  // prevents double submission per slot
  lives: number | null
  eliminated: boolean
}

// ── Question list catalog types ──────────────────────────────────────────────

export interface QuestionListRecord {
  id: string
  name: string
  description: string
  visibility: 'public' | 'private'
  owner_type: string
  owner_id: string
  created_at: string
  updated_at: string
}

export interface QuestionRecord {
  id: string
  question_list_id: string
  text: string
  options: Option[]
  correct_option_id: string
  order_index: number
}
