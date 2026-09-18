import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { TaskEvent } from '@/api/events'
import type { Task } from '@/api/types'

export interface ToolActivity {
  callId: string
  name: string
  status: 'running' | 'ok' | 'error' | 'denied'
  message: string
  at: number
}

export interface Approval {
  callId: string
  name: string
  input: unknown
}

// How many tool calls a task page keeps in view.
const maxActivity = 30

// Tasks (§4.12) as the task panel and the session lists show them: each
// task's record and status line, and — live from the event channel (§5.3) —
// its tool activity and the write calls waiting for the user.
export const useTasksStore = defineStore('tasks', () => {
  const tasks = ref<Record<string, Task>>({})
  const activity = ref<Record<string, ToolActivity[]>>({})
  const approvals = ref<Record<string, Approval[]>>({})
  const loaded = ref(false)

  async function load() {
    try {
      const list = await api.listTasks()
      const next: Record<string, Task> = {}
      const pending: Record<string, Approval[]> = {}
      for (const t of list) {
        next[t.id] = t
        pending[t.id] = (t.approvals ?? []).map((p) => ({ callId: p.callId, name: p.name, input: p.input }))
      }
      tasks.value = next
      approvals.value = pending
      loaded.value = true
    } catch {
      // The session list works without summaries.
    }
  }

  async function loadOne(id: string) {
    try {
      const [t, pending] = await Promise.all([api.getTask(id), api.taskApprovals(id)])
      tasks.value = { ...tasks.value, [id]: t }
      approvals.value = {
        ...approvals.value,
        [id]: pending.map((p) => ({ callId: p.callId, name: p.name, input: p.input })),
      }
    } catch {
      // Shown as missing by the panel.
    }
  }

  function applyEvent(ev: TaskEvent) {
    switch (ev.type) {
      case 'taskSummary': {
        const t = tasks.value[ev.taskId]
        if (t) tasks.value = { ...tasks.value, [ev.taskId]: { ...t, summary: ev.summary } }
        else void loadOne(ev.taskId)
        break
      }
      case 'taskTool': {
        const list = [...(activity.value[ev.taskId] ?? [])]
        const i = list.findIndex((a) => a.callId === ev.callId)
        const entry: ToolActivity = { callId: ev.callId, name: ev.name, status: ev.status, message: ev.message, at: Date.now() }
        if (i >= 0) list[i] = entry
        else list.unshift(entry)
        activity.value = { ...activity.value, [ev.taskId]: list.slice(0, maxActivity) }
        break
      }
      case 'taskApproval': {
        const rest = (approvals.value[ev.taskId] ?? []).filter((a) => a.callId !== ev.callId)
        if (ev.status === 'pending') rest.push({ callId: ev.callId, name: ev.name, input: ev.input })
        approvals.value = { ...approvals.value, [ev.taskId]: rest }
        break
      }
    }
  }

  async function decide(taskId: string, callId: string, approve: boolean) {
    await api.decideApproval(taskId, callId, approve)
    // The resolving event removes the card everywhere; remove it here too so
    // this browser doesn't wait for the round trip.
    approvals.value = {
      ...approvals.value,
      [taskId]: (approvals.value[taskId] ?? []).filter((a) => a.callId !== callId),
    }
  }

  function pendingCount(taskId: string | null | undefined): number {
    return taskId ? (approvals.value[taskId]?.length ?? 0) : 0
  }

  return { tasks, activity, approvals, loaded, load, loadOne, applyEvent, decide, pendingCount }
})
