import { reactive } from 'vue'

export type ActorType = 'admin' | 'user'

export interface Actor {
  type: ActorType
  id: string
}

// Global simulated identity — injected as X-Debug-Actor-Type / X-Debug-Actor-Id headers.
// This is a dev-only mechanism. Replace with real auth before production.
export const actor = reactive<Actor>({
  type: 'user',
  id: 'dev-user-1',
})
