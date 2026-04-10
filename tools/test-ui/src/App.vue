<script setup lang="ts">
import { ref, reactive } from 'vue'
import ActorBar from './components/ActorBar.vue'
import HealthPanel from './components/HealthPanel.vue'
import QuestionListsPanel from './components/QuestionListsPanel.vue'
import GamePanel from './components/GamePanel.vue'
import PlayerCard from './components/PlayerCard.vue'
import DebugPanel from './components/DebugPanel.vue'
import type { QuestionListRecord } from './types'

interface Slot { id: string }

const gameId = ref('')
const selectedList = ref<QuestionListRecord | null>(null)
const sidebarTab = ref<'lists' | 'game'>('lists')

const slots = reactive<Slot[]>([
  { id: crypto.randomUUID() },
  { id: crypto.randomUUID() },
])

function addSlot() {
  if (slots.length < 6) slots.push({ id: crypto.randomUUID() })
}

function removeSlot(id: string) {
  const idx = slots.findIndex(s => s.id === id)
  if (idx !== -1) slots.splice(idx, 1)
}

function onGameCreated(id: string, ownerId: string) {
  gameId.value = id
  console.info('[test-ui] game created', id, 'owner', ownerId)
}

function onGameIdChange(id: string) {
  gameId.value = id
}

function onListSelected(list: QuestionListRecord) {
  selectedList.value = list
  sidebarTab.value = 'game'
}
</script>

<template>
  <div class="layout">

    <!-- ── Header ── -->
    <header class="header">
      <span class="header-title">quizz-backend</span>
      <span class="muted">test ui</span>
      <div class="header-spacer" />
      <span v-if="selectedList" class="muted" style="font-size:11px">
        list: <span style="color:var(--blue)">{{ selectedList.name }}</span>
      </span>
      <span v-if="gameId" class="muted" style="font-size:11px; margin-left:8px">
        game: <span class="game-id">{{ gameId }}</span>
      </span>
    </header>

    <!-- ── Main ── -->
    <div class="main">

      <!-- Sidebar: controls -->
      <aside class="sidebar">
        <ActorBar />
        <HealthPanel />

        <!-- Tab bar -->
        <div class="tab-bar">
          <button
            class="tab-btn"
            :class="{ active: sidebarTab === 'lists' }"
            @click="sidebarTab = 'lists'"
          >Lists</button>
          <button
            class="tab-btn"
            :class="{ active: sidebarTab === 'game' }"
            @click="sidebarTab = 'game'"
          >
            Game
            <span v-if="gameId" class="tab-dot" />
          </button>
        </div>

        <QuestionListsPanel
          v-show="sidebarTab === 'lists'"
          @list-selected="onListSelected"
        />

        <GamePanel
          v-show="sidebarTab === 'game'"
          :gameId="gameId"
          :selectedList="selectedList"
          @game-created="onGameCreated"
          @game-id-change="onGameIdChange"
        />
      </aside>

      <!-- Content: player cards -->
      <main class="content">
        <div style="display:flex; align-items:center; gap:8px; margin-bottom:10px">
          <h2 style="margin:0">Players ({{ slots.length }})</h2>
          <button @click="addSlot" :disabled="slots.length >= 6" style="padding:2px 8px">
            + add player
          </button>
          <span class="muted" style="font-size:11px">
            max 6 · click events to expand payload
          </span>
        </div>

        <div class="player-grid">
          <PlayerCard
            v-for="(slot, idx) in slots"
            :key="slot.id"
            :gameId="gameId"
            :slotIndex="idx"
            @remove="removeSlot(slot.id)"
          />
        </div>
      </main>
    </div>

    <!-- ── Footer: debug ── -->
    <footer class="footer">
      <DebugPanel />
    </footer>

  </div>
</template>
