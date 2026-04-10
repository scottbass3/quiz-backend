<script setup lang="ts">
import { ref, computed, onUnmounted } from 'vue'
import { api } from '../api'
import type { WsEvent, EventLogEntry, ActiveQuestion, WsStatus } from '../types'

const props = defineProps<{
  gameId: string
  slotIndex: number
}>()

const emit = defineEmits<{ (e: 'remove'): void }>()

// ── Player state ──
const playerName = ref(`Player ${props.slotIndex + 1}`)
const playerId = ref('')
const joined = ref(false)
const joinError = ref('')
const lives = ref<number | null>(null)
const eliminated = ref(false)

// ── WebSocket state ──
const wsStatus = ref<WsStatus>('disconnected')
const ws = ref<WebSocket | null>(null)

// ── Events ──
const events = ref<EventLogEntry[]>([])
const expandedId = ref<string | null>(null)

// ── Current question from WS events ──
const activeQuestion = ref<ActiveQuestion | null>(null)
const answeredQuestionId = ref<string | null>(null)

// ── Join ──
async function join() {
  if (!props.gameId) { joinError.value = 'no game id set'; return }
  joinError.value = ''
  try {
    const res = await api.joinGame(props.gameId, playerName.value.trim() || `Player${props.slotIndex + 1}`)
    playerId.value = res.player_id
    joined.value = true
  } catch (e) {
    joinError.value = String(e)
  }
}

// ── WebSocket ──
function connect() {
  if (!props.gameId || !playerId.value) return
  if (ws.value) ws.value.close()

  wsStatus.value = 'connecting'
  const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws?gameId=${props.gameId}&playerId=${playerId.value}`
  const socket = new WebSocket(url)
  ws.value = socket

  socket.onopen = () => { wsStatus.value = 'connected' }
  socket.onclose = () => { wsStatus.value = 'disconnected'; ws.value = null }
  socket.onerror = () => { wsStatus.value = 'error' }

  socket.onmessage = (e) => {
    let event: WsEvent
    try { event = JSON.parse(e.data as string) }
    catch { event = { type: 'raw', payload: { data: e.data } } }

    const entry: EventLogEntry = {
      id: crypto.randomUUID(),
      ts: new Date().toLocaleTimeString(),
      event,
    }
    events.value.unshift(entry)
    if (events.value.length > 200) events.value.splice(200)

    handleEvent(event)
  }
}

function disconnect() {
  ws.value?.close()
  ws.value = null
}

function handleEvent(event: WsEvent) {
  switch (event.type) {
    case 'question_started': {
      const p = event.payload as ActiveQuestion | undefined
      if (p) {
        activeQuestion.value = p
        answeredQuestionId.value = null
      }
      break
    }
    case 'question_closed':
      activeQuestion.value = null
      break
    case 'life_lost': {
      const remaining = event.payload?.lives_left
      if (typeof remaining === 'number') lives.value = remaining
      break
    }
    case 'player_eliminated':
      if (event.payload?.player_id === playerId.value) {
        eliminated.value = true
        lives.value = 0
      }
      break
    case 'game_over':
      activeQuestion.value = null
      break
  }
}

// ── Submit answer via WS ──
function submitAnswer(optionId: string) {
  if (!ws.value || ws.value.readyState !== WebSocket.OPEN) return
  if (!activeQuestion.value) return
  if (answeredQuestionId.value === activeQuestion.value.question_id) return

  const payload = {
    type: 'submit_answer',
    data: {
      question_id: activeQuestion.value.question_id,
      option_id: optionId,
    },
  }
  ws.value.send(JSON.stringify(payload))
  answeredQuestionId.value = activeQuestion.value.question_id
}

// ── Option labels ──
const LABELS = ['A', 'B', 'C', 'D']

function optionLabel(index: number) {
  return LABELS[index] ?? String(index + 1)
}

function toggleExpand(id: string) {
  expandedId.value = expandedId.value === id ? null : id
}

const livesDisplay = computed(() => {
  if (lives.value === null) return null
  return Array.from({ length: 3 }, (_, i) => i < lives.value! ? '♥' : '♡')
})

onUnmounted(() => ws.value?.close())
</script>

<template>
  <div class="player-card" :class="{ eliminated }">

    <!-- Header -->
    <div class="player-card-header">
      <span class="ws-dot" :class="wsStatus" />
      <span style="flex:1; font-weight:bold">
        {{ playerName }}
      </span>
      <span v-if="playerId" class="muted" style="font-size:10px">{{ playerId.slice(0, 8) }}…</span>
      <span v-if="livesDisplay" class="lives">
        <span v-for="(h, i) in livesDisplay" :key="i" class="heart" :class="{ lost: h === '♡' }">{{ h }}</span>
      </span>
      <button class="danger" style="padding:1px 6px; font-size:11px" @click="emit('remove')">✕</button>
    </div>

    <div class="player-card-body">

      <!-- Name + join -->
      <div v-if="!joined">
        <div class="row" style="margin-bottom:4px">
          <input v-model="playerName" type="text" placeholder="player name" @keyup.enter="join" />
          <button class="primary" @click="join" :disabled="!gameId">join</button>
        </div>
        <div v-if="joinError" class="danger" style="font-size:11px">{{ joinError }}</div>
      </div>

      <div v-else>
        <div class="muted" style="font-size:11px; margin-bottom:4px">
          joined as <span style="color:var(--blue)">{{ playerName }}</span>
        </div>
      </div>

      <!-- WS controls -->
      <div class="btn-row">
        <button class="success" @click="connect" :disabled="!joined || wsStatus === 'connected'">
          connect ws
        </button>
        <button @click="disconnect" :disabled="wsStatus === 'disconnected'">
          disconnect
        </button>
        <span class="badge" :class="{
          ok: wsStatus === 'connected',
          warning: wsStatus === 'connecting',
          muted: wsStatus === 'disconnected',
          error: wsStatus === 'error',
        }">{{ wsStatus }}</span>
      </div>

      <!-- Active question -->
      <div v-if="activeQuestion" class="question-box">
        <div class="label">
          Q{{ activeQuestion.index + 1 }}/{{ activeQuestion.total }}
        </div>
        <div class="question-text">{{ activeQuestion.text }}</div>
        <div class="option-grid">
          <button
            v-for="(opt, idx) in activeQuestion.options"
            :key="opt.id"
            class="option-btn"
            :class="{ answered: answeredQuestionId === activeQuestion.question_id && answeredQuestionId !== null }"
            :disabled="
              wsStatus !== 'connected' ||
              eliminated ||
              answeredQuestionId === activeQuestion.question_id
            "
            @click="submitAnswer(opt.id)"
          >
            <span class="option-label">{{ optionLabel(idx) }}</span>{{ opt.text }}
          </button>
        </div>
        <div v-if="answeredQuestionId === activeQuestion.question_id" class="muted" style="font-size:11px; margin-top:4px">
          answer submitted ✓
        </div>
      </div>

      <!-- Event log -->
      <div>
        <div style="display:flex; align-items:center; gap:6px; margin-bottom:4px">
          <span class="label" style="margin:0">events ({{ events.length }})</span>
          <button style="padding:1px 6px; font-size:11px" @click="events.splice(0)">clear</button>
        </div>
        <div class="event-log">
          <div
            v-for="entry in events"
            :key="entry.id"
            class="event-entry"
            :class="entry.event.type"
            @click="toggleExpand(entry.id)"
          >
            <span class="event-ts">{{ entry.ts }}</span>
            <span class="event-type">{{ entry.event.type }}</span>
            <span
              v-if="entry.event.payload"
              class="event-payload"
              :class="{ expanded: expandedId === entry.id }"
            >{{ expandedId === entry.id
                ? JSON.stringify(entry.event.payload, null, 2)
                : JSON.stringify(entry.event.payload)
              }}</span>
          </div>
          <div v-if="events.length === 0" class="muted" style="font-size:11px; padding:4px">no events yet</div>
        </div>
      </div>

    </div>
  </div>
</template>
