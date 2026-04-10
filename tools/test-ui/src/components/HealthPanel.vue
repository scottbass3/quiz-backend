<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api'

const status = ref<{ status: string; uptime: string } | null>(null)
const error = ref('')
const loading = ref(false)

async function check() {
  loading.value = true
  error.value = ''
  status.value = null
  try {
    status.value = await api.health()
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="panel">
    <h2>Health</h2>
    <div class="btn-row">
      <button @click="check" :disabled="loading">
        {{ loading ? 'checking…' : 'GET /health' }}
      </button>
      <span v-if="status" class="badge ok">{{ status.status }} · {{ status.uptime }}</span>
      <span v-if="error" class="badge error">{{ error }}</span>
    </div>
  </div>
</template>
