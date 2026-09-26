import { ref } from 'vue'
import { api } from './api'
import type { ThemeRecord } from './types'

// Global themes, shared by the Themes tab and the question form.
export const globalThemes = ref<ThemeRecord[]>([])

export async function loadGlobalThemes(): Promise<void> {
  try {
    globalThemes.value = await api.listThemes()
  } catch {
    globalThemes.value = []
  }
}
