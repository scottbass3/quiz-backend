import { addHttpLog } from './debug'

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const url = `/api${path}`
  const t0 = Date.now()
  const id = crypto.randomUUID()

  const opts: RequestInit = {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  }

  let response: unknown
  let error: string | undefined

  try {
    const res = await fetch(url, opts)
    response = await res.json()
    if (!res.ok) {
      error = `HTTP ${res.status}`
      addHttpLog({ id, ts: new Date().toISOString(), method, url, body, response, error, durationMs: Date.now() - t0 })
      throw new Error(`${error}: ${JSON.stringify(response)}`)
    }
  } catch (e) {
    if (!error) error = String(e)
    addHttpLog({ id, ts: new Date().toISOString(), method, url, body, response, error, durationMs: Date.now() - t0 })
    throw e
  }

  addHttpLog({ id, ts: new Date().toISOString(), method, url, body, response, durationMs: Date.now() - t0 })
  return response as T
}

export const api = {
  health: () =>
    call<{ status: string; uptime: string }>('GET', '/health'),

  createGame: (ownerName: string) =>
    call<{ game_id: string; owner_id: string }>('POST', '/games', { owner_name: ownerName }),

  getGame: (id: string) =>
    call<Record<string, unknown>>('GET', `/games/${id}`),

  joinGame: (gameId: string, playerName: string) =>
    call<{ game_id: string; player_id: string }>('POST', `/games/${gameId}/join`, { player_name: playerName }),

  addQuestion: (gameId: string, text: string, options: { id: string; text: string }[], correctOptionId: string) =>
    call<{ question_id: string }>('POST', `/games/${gameId}/questions`, {
      text,
      options,
      correct_option_id: correctOptionId,
    }),

  startQuestion: (gameId: string) =>
    call<unknown>('POST', `/games/${gameId}/start`),

  closeQuestion: (gameId: string) =>
    call<{ life_lost: string[]; eliminated: string[]; game_over: boolean; winner: string }>('POST', `/games/${gameId}/close`),
}
