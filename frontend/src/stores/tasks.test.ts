import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { parseEvent } from '@/api/events'

const decideMock = vi.fn()
vi.mock('@/api/client', () => ({
  api: {
    listTasks: vi.fn().mockResolvedValue([]),
    getTask: vi.fn().mockResolvedValue({ id: 't1', spec: { name: 'x', agent: {} }, summary: '' }),
    taskApprovals: vi.fn().mockResolvedValue([]),
    decideApproval: (...args: unknown[]) => decideMock(...args),
  },
}))

const { useTasksStore } = await import('./tasks')

describe('task events', () => {
  it('parses the three task event types and drops malformed ones', () => {
    expect(parseEvent('{"type":"taskSummary","taskId":"t1","summary":"PR open"}')).toEqual({
      type: 'taskSummary',
      taskId: 't1',
      summary: 'PR open',
    })
    expect(parseEvent('{"type":"taskTool","taskId":"t1","callId":"c","name":"jira__read","status":"weird"}')).toMatchObject({
      status: 'error',
    })
    expect(parseEvent('{"type":"taskApproval","callId":"c"}')).toBeNull()
    expect(parseEvent('{"type":"taskApproval","taskId":"t1","callId":"c","name":"n","status":"pending","input":{"a":1}}')).toMatchObject({
      input: { a: 1 },
      status: 'pending',
    })
  })
})

describe('tasks store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    decideMock.mockReset()
  })

  it('tracks approvals from pending to resolved', async () => {
    const store = useTasksStore()
    store.applyEvent({ type: 'taskApproval', taskId: 't1', callId: 'c1', name: 'jira__comment', input: {}, status: 'pending' })
    store.applyEvent({ type: 'taskApproval', taskId: 't1', callId: 'c2', name: 'jira__create', input: {}, status: 'pending' })
    expect(store.pendingCount('t1')).toBe(2)
    store.applyEvent({ type: 'taskApproval', taskId: 't1', callId: 'c1', name: 'jira__comment', input: null, status: 'approved' })
    expect(store.pendingCount('t1')).toBe(1)

    decideMock.mockResolvedValue(undefined)
    await store.decide('t1', 'c2', false)
    expect(decideMock).toHaveBeenCalledWith('t1', 'c2', false)
    expect(store.pendingCount('t1')).toBe(0)
    expect(store.pendingCount(null)).toBe(0)
  })

  it('keeps one activity row per call, newest first, and updates summaries', () => {
    const store = useTasksStore()
    store.tasks = { t1: { id: 't1', summary: '' } as never }
    store.applyEvent({ type: 'taskTool', taskId: 't1', callId: 'a', name: 'x', status: 'running', message: '' })
    store.applyEvent({ type: 'taskTool', taskId: 't1', callId: 'b', name: 'y', status: 'running', message: '' })
    store.applyEvent({ type: 'taskTool', taskId: 't1', callId: 'a', name: 'x', status: 'ok', message: '' })
    expect(store.activity.t1.map((a) => `${a.callId}:${a.status}`)).toEqual(['b:running', 'a:ok'])
    store.applyEvent({ type: 'taskSummary', taskId: 't1', summary: 'CI green' })
    expect(store.tasks.t1.summary).toBe('CI green')
  })
})
