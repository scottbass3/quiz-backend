<script setup lang="ts">
import { ref, reactive } from 'vue'
import { api } from '../api'
import type { QuestionListRecord, QuestionRecord } from '../types'

const emit = defineEmits<{
  (e: 'list-selected', list: QuestionListRecord): void
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

async function selectList(list: QuestionListRecord) {
  selectedList.value = list
  loadingQ.value = true
  try {
    questions.value = await api.listQuestions(list.id)
  } catch (e) {
    questions.value = []
  } finally {
    loadingQ.value = false
  }
  emit('list-selected', list)
}

// ── Add question to selected list ────────────────────────────────────────────
const qText = ref('')
const qOptions = reactive([
  { id: 'a', text: '' },
  { id: 'b', text: '' },
  { id: 'c', text: '' },
  { id: 'd', text: '' },
])
const qCorrect = ref('a')
const addingQ = ref(false)
const addQMsg = ref('')

async function addQuestion() {
  if (!selectedList.value || !qText.value.trim()) return
  const filled = qOptions.filter(o => o.text.trim())
  if (filled.length < 2) { addQMsg.value = '2 options minimum'; return }
  if (!filled.find(o => o.id === qCorrect.value)) { addQMsg.value = 'correct option must be filled'; return }

  addingQ.value = true
  addQMsg.value = ''
  try {
    const res = await api.addQuestionToList(selectedList.value.id, qText.value.trim(), filled, qCorrect.value)
    addQMsg.value = `added: ${res.question_id.slice(0, 8)}…`
    qText.value = ''
    qOptions.forEach(o => (o.text = ''))
    qCorrect.value = 'a'
    await selectList(selectedList.value) // refresh question list
  } catch (e) {
    addQMsg.value = String(e)
  } finally {
    addingQ.value = false
  }
}
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
      <div class="label">
        selected: <span style="color:var(--blue)">{{ selectedList.name }}</span>
        <span class="muted" style="font-size:10px; margin-left:4px">({{ selectedList.visibility }})</span>
      </div>

      <!-- Questions in list -->
      <div style="display:flex; align-items:center; gap:6px; margin-bottom:4px">
        <span class="muted" style="font-size:11px">{{ questions.length }} question(s)</span>
        <button style="padding:1px 6px; font-size:11px" @click="selectList(selectedList!)">refresh</button>
      </div>
      <div class="list-scroll" style="margin-bottom:8px; max-height:100px">
        <div v-if="loadingQ" class="muted" style="font-size:11px; padding:4px">loading…</div>
        <div v-else-if="questions.length === 0" class="muted" style="font-size:11px; padding:4px">no questions yet</div>
        <div v-for="(q, i) in questions" :key="q.id" class="list-item" style="cursor:default">
          <span class="muted" style="font-size:10px">#{{ i + 1 }}</span>
          {{ q.text }}
          <span class="muted" style="font-size:10px; margin-left:4px">
            (correct: {{ q.options.find(o => o.id === q.correct_option_id)?.text ?? q.correct_option_id }})
          </span>
        </div>
      </div>

      <!-- Add question to list -->
      <div class="label">add question</div>
      <input v-model="qText" type="text" placeholder="question text" style="margin-bottom:6px" />
      <div class="option-inputs" style="margin-bottom:6px">
        <div v-for="opt in qOptions" :key="opt.id" class="option-row">
          <label>{{ opt.id }}</label>
          <input v-model="opt.text" type="text" :placeholder="`option ${opt.id}`" />
          <input type="radio" :value="opt.id" v-model="qCorrect" title="correct" />
        </div>
      </div>
      <div class="btn-row">
        <button @click="addQuestion" :disabled="addingQ || !qText.trim()">add question</button>
        <span v-if="addQMsg" style="font-size:11px"
          :class="addQMsg.includes('added') ? 'success' : 'danger'">{{ addQMsg }}</span>
      </div>
    </template>
  </div>
</template>
