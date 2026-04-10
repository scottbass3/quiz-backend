<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { actor, sessionUser, sessionLoading, sessionError, fetchSession } from '../actor'
import { api } from '../api'

onMounted(fetchSession)

// When the debug actor changes, re-fetch /auth/me so the displayed identity stays in sync.
// Has no effect when OIDC is enabled (session comes from the cookie, not these fields).
watch([() => actor.type, () => actor.id], fetchSession)

async function logout() {
  await api.logout()
  sessionUser.value = null
  window.location.href = '/api/auth/login'
}
</script>

<template>
  <div class="panel" style="padding: 8px 12px">

    <!-- Loading -->
    <div v-if="sessionLoading" class="muted" style="font-size:11px">checking auth…</div>

    <!-- OIDC enabled — not logged in -->
    <template v-else-if="!sessionUser">
      <div style="display:flex; align-items:center; gap:8px; flex-wrap:wrap">
        <span class="badge error">not logged in</span>
        <a
          href="/api/auth/login"
          style="color:var(--blue); font-size:12px; text-decoration:none; border:1px solid var(--blue); border-radius:4px; padding:3px 10px"
        >Login with OIDC</a>
      </div>
    </template>

    <!-- OIDC enabled — logged in -->
    <template v-else-if="sessionUser.oidc_enabled">
      <div style="display:flex; align-items:center; gap:8px; flex-wrap:wrap">
        <span class="badge" :class="sessionUser.actor_type === 'admin' ? 'warning' : 'ok'">
          {{ sessionUser.actor_type }}
        </span>
        <span style="font-size:12px; color:var(--text)">
          {{ sessionUser.name || sessionUser.email || sessionUser.sub }}
        </span>
        <div class="header-spacer" style="flex:1" />
        <button class="danger" style="font-size:11px; padding:2px 8px" @click="logout">
          logout
        </button>
      </div>
    </template>

    <!-- Dev mode (OIDC disabled) — debug controls -->
    <template v-else>
      <div style="display:flex; align-items:center; gap:8px; flex-wrap:wrap">
        <span class="label" style="margin:0; white-space:nowrap">actor (dev)</span>

        <label style="display:flex; align-items:center; gap:3px; font-size:12px">
          <input type="radio" value="admin" v-model="actor.type" />
          admin
        </label>
        <label style="display:flex; align-items:center; gap:3px; font-size:12px">
          <input type="radio" value="user" v-model="actor.type" />
          user
        </label>

        <input
          v-model="actor.id"
          type="text"
          placeholder="actor id"
          style="flex:1; min-width:100px; font-size:11px; padding:2px 6px"
        />

        <span
          class="badge"
          :class="actor.type === 'admin' ? 'warning' : 'ok'"
          style="font-size:10px; white-space:nowrap"
        >
          {{ actor.type }} · {{ actor.id }}
        </span>
      </div>
      <div class="muted" style="font-size:10px; margin-top:4px">
        Dev-only: injects <code>X-Debug-Actor-Type</code> + <code>X-Debug-Actor-Id</code> headers.
      </div>
    </template>

    <!-- Error -->
    <div v-if="sessionError" class="muted" style="font-size:10px; margin-top:3px; color:var(--red)">
      {{ sessionError }}
    </div>
  </div>
</template>
