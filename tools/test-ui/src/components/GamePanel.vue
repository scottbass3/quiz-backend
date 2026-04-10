<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api'
import type { QuestionListRecord } from '../types'

const emit = defineEmits<{
  (e: 'game-created', gameId: string, ownerId: string): void
  (e: 'game-id-change', gameId: string): void
}>()

const props = defineProps<{
  gameId: string
  selectedList: QuestionListRecord | null
}>()

// ── Create ──
const ownerName = ref('Host')
const creating = ref(false)
const createError = ref('')

async function createGame() {
  if (!props.selectedList) {
    createError.value = 'select a question list first'
    return
  }
  creating.value = true
  createError.value = ''
  try {
    const res = await api.createGame(ownerName.value.trim() || 'Host', props.selectedList.id)
    emit('game-created', res.game_id, res.owner_id)
  } catch (e) {
    createError.value = String(e)
  } finally {
    creating.value = false
  }
}

// ── Load state ──
const localId = ref('')
const gameState = ref<Record<string, unknown> | null>(null)
const loadError = ref('')
const loading = ref(false)

async function loadGame() {
  const id = localId.value.trim() || props.gameId
  if (!id) return
  loading.value = true
  loadError.value = ''
  try {
    gameState.value = await api.getGame(id)
    emit('game-id-change', id)
  } catch (e) {
    loadError.value = String(e)
  } finally {
    loading.value = false
  }
}

// ── Control ──
const controlling = ref(false)
const controlMsg = ref('')

async function startQuestion() {
  if (!props.gameId) return
  controlling.value = true
  controlMsg.value = ''
  try {
    await api.startQuestion(props.gameId)
    controlMsg.value = 'question started ✓'
  } catch (e) {
    controlMsg.value = String(e)
  } finally {
    controlling.value = false
  }
}

async function closeQuestion() {
  if (!props.gameId) return
  controlling.value = true
  controlMsg.value = ''
  try {
    const res = await api.closeQuestion(props.gameId)
    const parts: string[] = []
    if (res.life_lost?.length)  parts.push(`-1 life: ${res.life_lost.join(', ')}`)
    if (res.eliminated?.length) parts.push(`eliminated: ${res.eliminated.join(', ')}`)
    if (res.game_over)          parts.push(`game over → winner: ${res.winner || 'none'}`)
    controlMsg.value = parts.length ? parts.join(' | ') : 'closed ✓'
  } catch (e) {
    controlMsg.value = String(e)
  } finally {
    controlling.value = false
  }
}
</script>

<template>
  <div class="panel">
    <h2>Game</h2>

    <!-- Selected list summary -->
    <div style="margin-bottom: 8px; font-size: 11px">
      <span class="label" style="margin-right:4px">question list:</span>
      <span v-if="selectedList" style="color:var(--blue)">
        {{ selectedList.name }}
        <span class="muted">({{ selectedList.visibility }})</span>
      </span>
      <span v-else class="muted">— select a list in the panel above</span>
    </div>

    <!-- Create -->
    <div class="label">create game</div>
    <div class="row" style="margin-bottom: 8px">
      <input v-model="ownerName" type="text" placeholder="owner name" @keyup.enter="createGame" />
      <button class="primary" @click="createGame" :disabled="creating || !selectedList">create</button>
    </div>
    <div v-if="gameId" style="margin-bottom: 8px">
      <span class="label">game id</span>
      <span class="game-id">{{ gameId }}</span>
    </div>
    <div v-if="createError" class="danger" style="margin-bottom: 8px; font-size:11px">{{ createError }}</div>

    <!-- Load -->
    <div class="label">load / switch game</div>
    <div class="row" style="margin-bottom: 8px">
      <input v-model="localId" type="text" placeholder="game id" @keyup.enter="loadGame" />
      <button @click="loadGame" :disabled="loading">GET</button>
    </div>
    <div v-if="loadError" class="danger" style="margin-bottom: 6px; font-size:11px">{{ loadError }}</div>
    <div v-if="gameState" class="json-block">{{ JSON.stringify(gameState, null, 2) }}</div>

    <hr style="border-color: var(--border); margin: 10px 0" />

    <!-- Control -->
    <div class="label">question control <span class="muted">(active game: {{ gameId || '—' }})</span></div>
    <div class="btn-row" style="margin-bottom: 6px">
      <button class="success" @click="startQuestion" :disabled="controlling || !gameId">▶ start question</button>
      <button class="danger"  @click="closeQuestion" :disabled="controlling || !gameId">■ close question</button>
    </div>
    <div v-if="controlMsg" style="font-size:11px" :class="controlMsg.includes('error') || controlMsg.includes('Error') ? 'danger' : 'success'">
      {{ controlMsg }}
    </div>
  </div>
</template>
