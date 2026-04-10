<script setup lang="ts">
import { actor } from '../actor'
</script>

<template>
  <div class="panel" style="padding: 8px 12px">
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
      Dev-only: injects <code>X-Debug-Actor-Type</code> + <code>X-Debug-Actor-Id</code> headers on every HTTP request.
    </div>
  </div>
</template>
