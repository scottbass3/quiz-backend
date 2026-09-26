import { addHttpLog } from './debug'
import { actor } from './actor'
import type { QuestionListRecord, QuestionRecord, Option, GameOverReason, ThemeRecord } from './types'

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const url = `/api${path}`
  const t0 = Date.now()
  const id = crypto.randomUUID()

  // X-Debug-Actor-* headers are only used by the backend when OIDC is disabled.
  // When OIDC is enabled the session cookie takes precedence and these are ignored.
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
    // 204 No Content (e.g. DELETE) has no body to parse.
    response = res.status === 204 ? null : await res.json()
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

function themesPath(listId?: string): string {
  return listId ? `/question-lists/${listId}/themes` : '/themes'
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

  // themeFilter: a theme id, 'none' for questions without theme, or undefined for all.
  listQuestions: (listId: string, themeFilter?: string) =>
    call<QuestionRecord[]>(
      'GET',
      `/question-lists/${listId}/questions` + (themeFilter ? `?theme_id=${encodeURIComponent(themeFilter)}` : ''),
    ),

  // themeId: '' for no theme.
  addQuestionToList: (listId: string, text: string, options: Option[], correctOptionId: string, themeId = '') =>
    call<{ question_id: string }>('POST', `/question-lists/${listId}/questions`, {
      text,
      options,
      correct_option_id: correctOptionId,
      theme_id: themeId,
    }),

  // Replaces text, options, correct option and theme ('' removes the theme).
  updateQuestion: (listId: string, questionId: string, text: string, options: Option[], correctOptionId: string, themeId = '') =>
    call<QuestionRecord>('PUT', `/question-lists/${listId}/questions/${questionId}`, {
      text,
      options,
      correct_option_id: correctOptionId,
      theme_id: themeId,
    }),

  // ── Themes ─────────────────────────────────────────────────────────────────
  // listId undefined: global themes (/themes). Otherwise: custom themes of that list.
  listThemes: (listId?: string) =>
    call<ThemeRecord[]>('GET', themesPath(listId)),

  createTheme: (listId: string | undefined, name: string, description: string) =>
    call<ThemeRecord>('POST', themesPath(listId), { name, description }),

  updateTheme: (listId: string | undefined, themeId: string, name: string, description: string) =>
    call<ThemeRecord>('PUT', `${themesPath(listId)}/${themeId}`, { name, description }),

  deleteTheme: (listId: string | undefined, themeId: string) =>
    call<null>('DELETE', `${themesPath(listId)}/${themeId}`),

  // ── Games ──────────────────────────────────────────────────────────────────
  createGame: (
    ownerName: string,
    questionListId: string,
    options?: { initialLives?: number; answerTimeoutSeconds?: number },
  ) => {
    const body: Record<string, unknown> = { owner_name: ownerName, question_list_id: questionListId }
    if (options?.initialLives !== undefined) body.initial_lives = options.initialLives
    if (options?.answerTimeoutSeconds !== undefined) body.answer_timeout_seconds = options.answerTimeoutSeconds
    return call<{ game_id: string; owner_id: string; question_list_id: string; total_questions: number }>(
      'POST', '/games', body,
    )
  },

  getGame: (id: string) =>
    call<Record<string, unknown>>('GET', `/games/${id}`),

  joinGame: (gameId: string, playerName: string) =>
    call<{ game_id: string; player_id: string }>('POST', `/games/${gameId}/join`, { player_name: playerName }),

  startQuestion: (gameId: string) =>
    call<unknown>('POST', `/games/${gameId}/start`),

  closeQuestion: (gameId: string) =>
    call<{
      life_lost: string[] | null
      eliminated: string[] | null
      game_over: boolean
      winner: string
      survivors: string[] | null
      reason: GameOverReason | ''
      remaining_questions: number
    }>(
      'POST', `/games/${gameId}/close`
    ),

  // ── Auth ───────────────────────────────────────────────────────────────────
  logout: () =>
    call<{ status: string }>('POST', '/auth/logout'),
}
