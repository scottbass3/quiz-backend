import { reactive } from 'vue'
import type { HttpLogEntry } from './types'

export const httpLogs = reactive<HttpLogEntry[]>([])

export function addHttpLog(entry: HttpLogEntry) {
  httpLogs.unshift(entry)
  if (httpLogs.length > 100) httpLogs.splice(100)
}
