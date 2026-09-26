<script setup lang="ts">
// Manages one theme scope: global themes when listId is not set,
// otherwise the custom themes of that question list.
import { ref, computed, watch, onMounted } from 'vue'
import { api } from '../api'
import { globalThemes, loadGlobalThemes } from '../themes'
import type { ThemeRecord } from '../types'

const props = defineProps<{ listId?: string }>()
const emit = defineEmits<{ (e: 'changed', themes: ThemeRecord[]): void }>()

const listThemes = ref<ThemeRecord[]>([])
const themes = computed(() => (props.listId ? listThemes.value : globalThemes.value))
const loading = ref(false)
const msg = ref('')
const msgOk = ref(true)

function report(text: string, ok: boolean) {
  msg.value = text
  msgOk.value = ok
}

async function load() {
  loading.value = true
  try {
    if (props.listId) {
      listThemes.value = await api.listThemes(props.listId).catch(() => [])
    } else {
      await loadGlobalThemes()
    }
    emit('changed', themes.value)
  } finally {
    loading.value = false
  }
}

// ── Create ──
const newName = ref('')
const newDesc = ref('')

async function create() {
  if (!newName.value.trim()) return
  try {
    const t = await api.createTheme(props.listId, newName.value.trim(), newDesc.value.trim())
    report(`created: ${t.name}`, true)
    newName.value = ''
    newDesc.value = ''
    await load()
  } catch (e) {
    report(String(e), false)
  }
}

// ── Edit ──
const editingId = ref<string | null>(null)
const editName = ref('')
const editDesc = ref('')

function startEdit(t: ThemeRecord) {
  editingId.value = t.id
  editName.value = t.name
  editDesc.value = t.description
}

async function saveEdit() {
  if (!editingId.value) return
  try {
    const t = await api.updateTheme(props.listId, editingId.value, editName.value.trim(), editDesc.value.trim())
    report(`updated: ${t.name}`, true)
    editingId.value = null
    await load()
  } catch (e) {
    report(String(e), false)
  }
}

// ── Delete ──
async function remove(t: ThemeRecord) {
  try {
    await api.deleteTheme(props.listId, t.id)
    report(`deleted: ${t.name} (its questions are now without theme)`, true)
    await load()
  } catch (e) {
    report(String(e), false)
  }
}

watch(() => props.listId, () => { editingId.value = null; msg.value = ''; load() })
onMounted(load)
</script>

<template>
  <div>
    <div style="display:flex; align-items:center; gap:6px; margin-bottom:4px">
      <span class="label" style="margin:0">{{ listId ? 'list themes' : 'global themes' }}</span>
      <span class="muted" style="font-size:10px">{{ listId ? '(list editors)' : '(admin)' }}</span>
      <button style="padding:1px 6px; font-size:11px" @click="load" :disabled="loading">
        {{ loading ? '…' : 'refresh' }}
      </button>
    </div>

    <div class="list-scroll" style="margin-bottom:6px; max-height:120px">
      <div v-if="themes.length === 0" class="muted" style="font-size:11px; padding:4px">none</div>
      <div v-for="t in themes" :key="t.id" class="list-item" style="cursor:default">
        <template v-if="editingId === t.id">
          <input v-model="editName" type="text" placeholder="name" style="margin-bottom:3px" @keyup.enter="saveEdit" />
          <input v-model="editDesc" type="text" placeholder="description" style="margin-bottom:3px" @keyup.enter="saveEdit" />
          <div class="btn-row">
            <button class="primary" style="padding:1px 6px; font-size:11px" @click="saveEdit">save</button>
            <button style="padding:1px 6px; font-size:11px" @click="editingId = null">cancel</button>
          </div>
        </template>
        <template v-else>
          <div style="display:flex; align-items:center; gap:4px">
            <span style="flex:1">
              {{ t.name }}
              <span v-if="t.description" class="muted" style="font-size:10px"> · {{ t.description }}</span>
            </span>
            <button style="padding:0 5px; font-size:10px" @click="startEdit(t)">edit</button>
            <button class="danger" style="padding:0 5px; font-size:10px" @click="remove(t)">✕</button>
          </div>
        </template>
      </div>
    </div>

    <div class="row" style="margin-bottom:4px">
      <input v-model="newName" type="text" placeholder="theme name" @keyup.enter="create" />
      <input v-model="newDesc" type="text" placeholder="description (optional)" @keyup.enter="create" />
      <button class="primary" @click="create" :disabled="!newName.trim()">add</button>
    </div>
    <div v-if="msg" style="font-size:11px" :class="msgOk ? 'success' : 'danger'">{{ msg }}</div>
  </div>
</template>
