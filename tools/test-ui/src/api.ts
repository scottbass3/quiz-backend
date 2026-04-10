import { addHttpLog } from './debug'
import { actor } from './actor'
import type { QuestionListRecord, QuestionRecord, Option } from './types'

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const url = `/api${path}`
  const t0 = Date.now()
  const id = crypto.randomUUID()

  // Inject simulated identity headers on every request.
  const headers: Record<string, string> = {
    'X-Debug-Actor-Type': actor.type,
    'X-Debug-Actor-Id': actor.id,
  }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }

  const opts: RequestInit = {
    method,
    headers,
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
  // ── Health ─────────────────────────────────────────────────────────────────
  health: () =>
    call<{ status: string; uptime: string }>('GET', '/health'),

  // ── Question lists ─────────────────────────────────────────────────────────
  createQuestionList: (name: string, description: string, visibility: 'public' | 'private') =>
    call<QuestionListRecord>('POST', '/question-lists', { name, description, visibility }),

  listPublicQuestionLists: () =>
    call<QuestionListRecord[]>('GET', '/question-lists/public'),

  listPrivateQuestionLists: () =>
    call<QuestionListRecord[]>('GET', '/question-lists/private'),

  getQuestionList: (id: string) =>
    call<QuestionListRecord>('GET', `/question-lists/${id}`),

  listQuestions: (listId: string) =>
    call<QuestionRecord[]>('GET', `/question-lists/${listId}/questions`),

  addQuestionToList: (listId: string, text: string, options: Option[], correctOptionId: string) =>
    call<{ question_id: string }>('POST', `/question-lists/${listId}/questions`, {
      text,
      options,
      correct_option_id: correctOptionId,
    }),

  // ── Games ──────────────────────────────────────────────────────────────────
  createGame: (ownerName: string, questionListId: string) =>
    call<{ game_id: string; owner_id: string; question_list_id: string; total_questions: number }>(
      'POST', '/games', { owner_name: ownerName, question_list_id: questionListId }
    ),

  getGame: (id: string) =>
    call<Record<string, unknown>>('GET', `/games/${id}`),

  joinGame: (gameId: string, playerName: string) =>
    call<{ game_id: string; player_id: string }>('POST', `/games/${gameId}/join`, { player_name: playerName }),

  startQuestion: (gameId: string) =>
    call<unknown>('POST', `/games/${gameId}/start`),

  closeQuestion: (gameId: string) =>
    call<{ life_lost: string[]; eliminated: string[]; game_over: boolean; winner: string }>(
      'POST', `/games/${gameId}/close`
    ),
}
