<script setup lang="ts">
import { ref } from 'vue'
import { httpLogs } from '../debug'

const expandedId = ref<string | null>(null)
const collapsed = ref(false)

const httpBase = `${window.location.protocol}//${window.location.host}`
const wsBase   = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`

function toggle(id: string) {
  expandedId.value = expandedId.value === id ? null : id
}
</script>

<template>
  <div>
    <div
      style="display:flex; align-items:center; gap:8px; padding: 6px 16px; border-top: 1px solid var(--border); cursor:pointer; background: var(--surface)"
      @click="collapsed = !collapsed"
    >
      <span class="muted" style="font-size:11px; text-transform:uppercase; letter-spacing:.08em">
        {{ collapsed ? '▶' : '▼' }} debug — http log ({{ httpLogs.length }})
      </span>
      <span class="muted" style="font-size:10px">proxy → {{ httpBase }}</span>
      <span class="muted" style="font-size:10px">ws → {{ wsBase }}</span>
      <span style="flex:1" />
      <button
        style="padding:1px 6px; font-size:10px"
        @click.stop="httpLogs.splice(0)"
      >clear</button>
    </div>

    <div v-if="!collapsed" class="http-log" style="padding: 0 16px 8px">
      <div
        v-for="entry in httpLogs"
        :key="entry.id"
        class="http-entry"
        :class="{ err: !!entry.error }"
        @click="toggle(entry.id)"
      >
        <span class="http-method" :class="entry.method">{{ entry.method }}</span>
        <span class="http-url">{{ entry.url }}</span>
        <span class="http-ts">{{ entry.ts.slice(11, 23) }}</span>
        <span class="http-dur">{{ entry.durationMs }}ms</span>
        <span v-if="entry.error" class="danger" style="margin-left:8px; font-size:11px">{{ entry.error }}</span>

        <div v-if="expandedId === entry.id" class="http-detail">
          <div v-if="entry.body">
            <span class="muted">request:</span>
            {{ JSON.stringify(entry.body, null, 2) }}
          </div>
          <div v-if="entry.response">
            <span class="muted">response:</span>
            {{ JSON.stringify(entry.response, null, 2) }}
          </div>
        </div>
      </div>
      <div v-if="httpLogs.length === 0" class="muted" style="padding:6px; font-size:11px">no requests yet</div>
    </div>
  </div>
</template>
