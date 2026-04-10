import { reactive, ref } from 'vue'

export type ActorType = 'admin' | 'user'

export interface Actor {
  type: ActorType
  id: string
}

export interface SessionUser {
  sub: string
  name: string
  email: string
  actor_type: ActorType
  oidc_enabled: boolean
}

// Debug actor — injected as X-Debug-Actor-* headers when OIDC is disabled.
export const actor = reactive<Actor>({
  type: 'user',
  id: 'dev-user-1',
})

// Resolved session from /auth/me — null when unauthenticated (OIDC mode) or not yet loaded.
export const sessionUser = ref<SessionUser | null>(null)
export const sessionLoading = ref(true)
export const sessionError = ref<string | null>(null)

export async function fetchSession(): Promise<void> {
  sessionLoading.value = true
  sessionError.value = null
  try {
    const res = await fetch('/api/auth/me', {
      headers: {
        'X-Debug-Actor-Type': actor.type,
        'X-Debug-Actor-Id': actor.id,
      },
    })
    if (res.status === 401) {
      sessionUser.value = null
      return
    }
    if (!res.ok) {
      sessionError.value = `HTTP ${res.status}`
      return
    }
    sessionUser.value = await res.json()
  } catch (e) {
    sessionError.value = String(e)
  } finally {
    sessionLoading.value = false
  }
}
