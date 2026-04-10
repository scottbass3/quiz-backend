<script setup lang="ts">
import { ref, reactive } from 'vue'
import HealthPanel from './components/HealthPanel.vue'
import GamePanel from './components/GamePanel.vue'
import PlayerCard from './components/PlayerCard.vue'
import DebugPanel from './components/DebugPanel.vue'

interface Slot { id: string }

const gameId = ref('')

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
</script>

<template>
  <div class="layout">

    <!-- ── Header ── -->
    <header class="header">
      <span class="header-title">quizz-backend</span>
      <span class="muted">test ui</span>
      <div class="header-spacer" />
      <span v-if="gameId" class="muted" style="font-size:11px">
        game: <span class="game-id">{{ gameId }}</span>
      </span>
    </header>

    <!-- ── Main ── -->
    <div class="main">

      <!-- Sidebar: game controls -->
      <aside class="sidebar">
        <HealthPanel />
        <GamePanel
          :gameId="gameId"
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
