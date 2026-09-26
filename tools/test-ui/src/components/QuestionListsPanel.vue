<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { api } from '../api'
import { globalThemes, loadGlobalThemes } from '../themes'
import ThemesPanel from './ThemesPanel.vue'
import type { QuestionListRecord, QuestionRecord, ThemeRecord } from '../types'

const emit = defineEmits<{
  (e: 'list-selected', list: QuestionListRecord): void
  (e: 'use-in-game', list: QuestionListRecord): void
}>()

// ── Create list ──────────────────────────────────────────────────────────────
const newListName = ref('')
const newListDesc = ref('')
const newListVis = ref<'public' | 'private'>('public')
const creating = ref(false)
const createMsg = ref('')

async function createList() {
  if (!newListName.value.trim()) return
  creating.value = true
  createMsg.value = ''
  try {
    const res = await api.createQuestionList(newListName.value.trim(), newListDesc.value.trim(), newListVis.value)
    createMsg.value = `created: ${res.id.slice(0, 8)}…`
    newListName.value = ''
    newListDesc.value = ''
    await loadPublic()
    await loadPrivate()
  } catch (e) {
    createMsg.value = String(e)
  } finally {
    creating.value = false
  }
}

// ── List public ──────────────────────────────────────────────────────────────
const publicLists = ref<QuestionListRecord[]>([])
const loadingPublic = ref(false)

async function loadPublic() {
  loadingPublic.value = true
  try {
    publicLists.value = await api.listPublicQuestionLists()
  } catch (e) {
    publicLists.value = []
  } finally {
    loadingPublic.value = false
  }
}

// ── List private ─────────────────────────────────────────────────────────────
const privateLists = ref<QuestionListRecord[]>([])
const loadingPrivate = ref(false)

async function loadPrivate() {
  loadingPrivate.value = true
  try {
    privateLists.value = await api.listPrivateQuestionLists()
  } catch (e) {
    privateLists.value = []
  } finally {
    loadingPrivate.value = false
  }
}

// ── Selected list detail ─────────────────────────────────────────────────────
const selectedList = ref<QuestionListRecord | null>(null)
const questions = ref<QuestionRecord[]>([])
const loadingQ = ref(false)
// '' = all questions, 'none' = without theme, otherwise a theme id.
const themeFilter = ref('')
// Custom themes of the selected list, kept in sync by its ThemesPanel.
const listThemes = ref<ThemeRecord[]>([])

function selectList(list: QuestionListRecord) {
  selectedList.value = list
  themeFilter.value = ''
  listThemes.value = []
  resetForm()
  loadQuestions()
  loadGlobalThemes()
  emit('list-selected', list)
}

async function loadQuestions() {
  if (!selectedList.value) return
  loadingQ.value = true
  try {
    questions.value = await api.listQuestions(selectedList.value.id, themeFilter.value || undefined)
  } catch (e) {
    questions.value = []
  } finally {
    loadingQ.value = false
  }
}

function onListThemesChanged(themes: ThemeRecord[]) {
  listThemes.value = themes
  loadQuestions() // a renamed or deleted theme changes how questions display
}

// ── Add / edit question ──────────────────────────────────────────────────────
const DEFAULT_IDS = ['a', 'b', 'c', 'd']
const qText = ref('')
const qOptions = reactive(DEFAULT_IDS.map(id => ({ id, text: '' })))
const qCorrect = ref('a')
const qTheme = ref('') // '' = no theme
const editingId = ref<string | null>(null)
const savingQ = ref(false)
const qMsg = ref('')
const qMsgOk = ref(true)

function resetForm() {
  editingId.value = null
  qText.value = ''
  qOptions.forEach((o, i) => { o.id = DEFAULT_IDS[i]; o.text = '' })
  qCorrect.value = 'a'
  qTheme.value = ''
}

function startEdit(q: QuestionRecord) {
  resetForm()
  editingId.value = q.id
  qText.value = q.text
  // Keep the question's own option ids so correct_option_id stays valid.
  q.options.slice(0, qOptions.length).forEach((o, i) => {
    qOptions[i].id = o.id
    qOptions[i].text = o.text
  })
  // Give the remaining empty slots ids that do not collide with the question's.
  const used = new Set(q.options.map(o => o.id))
  const free = DEFAULT_IDS.filter(id => !used.has(id))
  for (let i = q.options.length; i < qOptions.length; i++) {
    qOptions[i].id = free.shift() ?? `opt${i + 1}`
  }
  qCorrect.value = q.correct_option_id
  qTheme.value = q.theme?.id ?? ''
  qMsg.value = q.options.length > qOptions.length
    ? `only the first ${qOptions.length} options can be edited here`
    : ''
  qMsgOk.value = false
}

async function saveQuestion() {
  if (!selectedList.value || !qText.value.trim()) return
  const filled = qOptions.filter(o => o.text.trim()).map(o => ({ id: o.id, text: o.text.trim() }))
  if (filled.length < 2) { qMsg.value = '2 options minimum'; qMsgOk.value = false; return }
  if (!filled.find(o => o.id === qCorrect.value)) { qMsg.value = 'correct option must be filled'; qMsgOk.value = false; return }

  savingQ.value = true
  qMsg.value = ''
  try {
    if (editingId.value) {
      await api.updateQuestion(selectedList.value.id, editingId.value, qText.value.trim(), filled, qCorrect.value, qTheme.value)
      qMsg.value = 'updated ✓'
    } else {
      const res = await api.addQuestionToList(selectedList.value.id, qText.value.trim(), filled, qCorrect.value, qTheme.value)
      qMsg.value = `added: ${res.question_id.slice(0, 8)}…`
    }
    qMsgOk.value = true
    resetForm()
    await loadQuestions()
  } catch (e) {
    qMsg.value = String(e)
    qMsgOk.value = false
  } finally {
    savingQ.value = false
  }
}

onMounted(loadGlobalThemes)
</script>

<template>
  <div class="panel">
    <h2>Question Lists</h2>

    <!-- Create -->
    <div class="label">create list</div>
    <div style="margin-bottom: 6px">
      <input v-model="newListName" type="text" placeholder="list name" style="margin-bottom: 4px" />
      <input v-model="newListDesc" type="text" placeholder="description (optional)" style="margin-bottom: 4px" />
      <div class="row" style="margin-bottom: 4px">
        <label style="display:flex; align-items:center; gap:4px; font-size:12px">
          <input type="radio" value="public" v-model="newListVis" /> public (admin)
        </label>
        <label style="display:flex; align-items:center; gap:4px; font-size:12px">
          <input type="radio" value="private" v-model="newListVis" /> private (user)
        </label>
      </div>
      <button class="primary" @click="createList" :disabled="creating || !newListName.trim()">create</button>
      <span v-if="createMsg" style="font-size:11px; margin-left:6px"
        :class="createMsg.includes('created') ? 'success' : 'danger'">
        {{ createMsg }}
      </span>
    </div>

    <hr style="border-color: var(--border); margin: 8px 0" />

    <!-- Public lists -->
    <div style="display:flex; align-items:center; gap:6px; margin-bottom:4px">
      <span class="label" style="margin:0">public lists</span>
      <button style="padding:1px 6px; font-size:11px" @click="loadPublic" :disabled="loadingPublic">
        {{ loadingPublic ? '…' : 'refresh' }}
      </button>
    </div>
    <div class="list-scroll" style="margin-bottom:8px">
      <div v-if="publicLists.length === 0" class="muted" style="font-size:11px; padding:4px">none</div>
      <div
        v-for="l in publicLists" :key="l.id"
        class="list-item" :class="{ selected: selectedList?.id === l.id }"
        @click="selectList(l)"
      >
        <span style="font-weight:bold">{{ l.name }}</span>
        <span class="muted" style="font-size:10px; margin-left:4px">{{ l.id.slice(0, 8) }}…</span>
        <span v-if="l.description" class="muted" style="font-size:10px; display:block">{{ l.description }}</span>
      </div>
    </div>

    <!-- Private lists -->
    <div style="display:flex; align-items:center; gap:6px; margin-bottom:4px">
      <span class="label" style="margin:0">my private lists</span>
      <button style="padding:1px 6px; font-size:11px" @click="loadPrivate" :disabled="loadingPrivate">
        {{ loadingPrivate ? '…' : 'refresh' }}
      </button>
    </div>
    <div class="list-scroll" style="margin-bottom:8px">
      <div v-if="privateLists.length === 0" class="muted" style="font-size:11px; padding:4px">none</div>
      <div
        v-for="l in privateLists" :key="l.id"
        class="list-item" :class="{ selected: selectedList?.id === l.id }"
        @click="selectList(l)"
      >
        <span style="font-weight:bold">{{ l.name }}</span>
        <span class="muted" style="font-size:10px; margin-left:4px">{{ l.id.slice(0, 8) }}…</span>
        <span v-if="l.description" class="muted" style="font-size:10px; display:block">{{ l.description }}</span>
      </div>
    </div>

    <!-- Selected list detail -->
    <template v-if="selectedList">
      <hr style="border-color: var(--border); margin: 8px 0" />
      <div style="display:flex; align-items:center; gap:6px; margin-bottom:6px">
        <span class="label" style="margin:0; flex:1">
          selected: <span style="color:var(--blue)">{{ selectedList.name }}</span>
          <span class="muted" style="font-size:10px; margin-left:4px">({{ selectedList.visibility }})</span>
        </span>
        <button class="primary" style="padding:1px 8px; font-size:11px" @click="emit('use-in-game', selectedList!)">
          use in game →
        </button>
      </div>

      <!-- Questions in list -->
      <div style="display:flex; align-items:center; gap:6px; margin-bottom:4px">
        <span class="muted" style="font-size:11px">{{ questions.length }} question(s)</span>
        <select v-model="themeFilter" @change="loadQuestions" style="flex:1; font-size:11px" title="filter by theme">
          <option value="">all themes</option>
          <option value="none">no theme</option>
          <optgroup v-if="globalThemes.length" label="global">
            <option v-for="t in globalThemes" :key="t.id" :value="t.id">{{ t.name }}</option>
          </optgroup>
          <optgroup v-if="listThemes.length" label="this list">
            <option v-for="t in listThemes" :key="t.id" :value="t.id">{{ t.name }}</option>
          </optgroup>
        </select>
        <button style="padding:1px 6px; font-size:11px" @click="loadQuestions">refresh</button>
      </div>
      <div class="list-scroll" style="margin-bottom:8px; max-height:140px">
        <div v-if="loadingQ" class="muted" style="font-size:11px; padding:4px">loading…</div>
        <div v-else-if="questions.length === 0" class="muted" style="font-size:11px; padding:4px">no questions</div>
        <div v-for="q in questions" :key="q.id" class="list-item" style="cursor:default"
          :class="{ selected: editingId === q.id }">
          <div style="display:flex; align-items:flex-start; gap:4px">
            <span style="flex:1">
              <span class="muted" style="font-size:10px">#{{ q.order_index + 1 }}</span>
              {{ q.text }}
              <span v-if="q.theme" class="theme-tag" :class="q.theme.scope">{{ q.theme.name }}</span>
              <span class="muted" style="font-size:10px; margin-left:4px">
                (correct: {{ q.options.find(o => o.id === q.correct_option_id)?.text ?? q.correct_option_id }})
              </span>
            </span>
            <button style="padding:0 5px; font-size:10px" @click="startEdit(q)">edit</button>
          </div>
        </div>
      </div>

      <!-- Add / edit question -->
      <div class="label">{{ editingId ? 'edit question' : 'add question' }}</div>
      <input v-model="qText" type="text" placeholder="question text" style="margin-bottom:6px" />
      <div class="option-inputs" style="margin-bottom:6px">
        <div v-for="opt in qOptions" :key="opt.id" class="option-row">
          <label>{{ opt.id }}</label>
          <input v-model="opt.text" type="text" :placeholder="`option ${opt.id}`" />
          <input type="radio" :value="opt.id" v-model="qCorrect" title="correct" />
        </div>
      </div>
      <div class="row" style="margin-bottom:6px">
        <span class="muted" style="font-size:11px">theme</span>
        <select v-model="qTheme" style="flex:1">
          <option value="">no theme</option>
          <optgroup v-if="globalThemes.length" label="global">
            <option v-for="t in globalThemes" :key="t.id" :value="t.id">{{ t.name }}</option>
          </optgroup>
          <optgroup v-if="listThemes.length" label="this list">
            <option v-for="t in listThemes" :key="t.id" :value="t.id">{{ t.name }}</option>
          </optgroup>
        </select>
      </div>
      <div class="btn-row">
        <button @click="saveQuestion" :disabled="savingQ || !qText.trim()">
          {{ editingId ? 'save question' : 'add question' }}
        </button>
        <button v-if="editingId" @click="resetForm">cancel</button>
        <span v-if="qMsg" style="font-size:11px" :class="qMsgOk ? 'success' : 'danger'">{{ qMsg }}</span>
      </div>

      <hr style="border-color: var(--border); margin: 8px 0" />

      <!-- Custom themes of this list -->
      <ThemesPanel :listId="selectedList.id" @changed="onListThemesChanged" />
    </template>
  </div>
</template>
