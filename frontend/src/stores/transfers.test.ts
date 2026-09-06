import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useTransfersStore } from './transfers'
import { emitHostopEvent } from '@/composables/useHostopEvents'
import { UploadAbortedError } from '@/api/upload'

const uploadMock = vi.hoisted(() => vi.fn())
vi.mock('@/api/upload', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/upload')>()
  return { ...actual, uploadHostFile: uploadMock }
})

vi.mock('@/api/client', () => ({
  api: { hostopStatus: vi.fn().mockResolvedValue({ status: 'running' }) },
}))

beforeEach(() => {
  setActivePinia(createPinia())
  uploadMock.mockReset()
  vi.useFakeTimers()
})
afterEach(() => vi.useRealTimers())

// The whole point of the store: state that outlives the component showing it.
// FileExplorerPanel is unmounted by a tab switch and by closing the panel,
// while the upload keeps running and the server-side Delete/Copy doesn't know
// the client exists.
describe('state survives the component', () => {
  it('keeps a running upload readable after the panel would have unmounted', async () => {
    let settle: () => void = () => {}
    uploadMock.mockReturnValue({
      promise: new Promise<void>((resolve) => (settle = resolve)),
      abort: vi.fn(),
    })
    const store = useTransfersStore()

    const done = store.startUpload('s1', 'a.bin', new File(['x'], 'a.bin'))
    expect(store.uploads['s1']?.status).toBe('running')

    // A component reading the store later — after unmount and remount — sees
    // the same in-flight upload rather than nothing.
    expect(store.uploadFor('s1')?.name).toBe('a.bin')

    settle()
    await done
    expect(store.uploads['s1']).toBeUndefined()
  })

  it('applies hostop events to an op nobody is currently rendering', async () => {
    const store = useTransfersStore()
    await store.startOp('s1', 'delete', 'dir', async () => ({ opId: 'op-1' }))

    emitHostopEvent({ type: 'hostopProgress', sessionId: 's1', opId: 'op-1', done: 3, total: 10 })
    expect(store.ops['s1']?.done).toBe(3)

    emitHostopEvent({ type: 'hostopDone', sessionId: 's1', opId: 'op-1', status: 'ok', message: '' })
    expect(store.ops['s1']?.status).toBe('ok')
  })

  it('keeps two sessions apart', async () => {
    const store = useTransfersStore()
    await store.startOp('s1', 'delete', 'one', async () => ({ opId: 'op-1' }))
    await store.startOp('s2', 'copy', 'two', async () => ({ opId: 'op-2' }))

    // Deliberately the *second* session's event: a lookup that ignored
    // sessionId would find s1 first and apply it there.
    emitHostopEvent({ type: 'hostopProgress', sessionId: 's2', opId: 'op-2', done: 7, total: 9 })
    expect(store.ops['s2']?.done).toBe(7)
    expect(store.ops['s1']?.done).toBe(0)

    emitHostopEvent({ type: 'hostopDone', sessionId: 's2', opId: 'op-2', status: 'ok', message: '' })
    expect(store.ops['s2']?.status).toBe('ok')
    expect(store.ops['s1']?.status).toBe('running')
  })
})

// A cancelled upload is not a failed one: the server removes its own staging
// file, so there is nothing to report and nothing to clean up client-side.
describe('cancelling an upload', () => {
  it('reports not-stored rather than throwing', async () => {
    const abort = vi.fn()
    let fail: (e: unknown) => void = () => {}
    uploadMock.mockReturnValue({
      promise: new Promise<void>((_, reject) => (fail = reject)),
      abort,
    })
    const store = useTransfersStore()

    const done = store.startUpload('s1', 'big.bin', new File(['x'], 'big.bin'))
    store.cancelUpload('s1')
    expect(abort).toHaveBeenCalled()

    fail(new UploadAbortedError())
    await expect(done).resolves.toBe(false)
    expect(store.uploads['s1']?.status).toBe('cancelled')
  })

  it('still reports a real failure as an error', async () => {
    let fail: (e: unknown) => void = () => {}
    uploadMock.mockReturnValue({
      promise: new Promise<void>((_, reject) => (fail = reject)),
      abort: vi.fn(),
    })
    const store = useTransfersStore()

    const done = store.startUpload('s1', 'big.bin', new File(['x'], 'big.bin'))
    fail(new Error('disk full'))
    await expect(done).rejects.toThrow('disk full')
    expect(store.uploads['s1']?.status).toBe('error')
  })

  it('clears a settled upload so the toolbar button unblocks', async () => {
    let fail: (e: unknown) => void = () => {}
    uploadMock.mockReturnValue({
      promise: new Promise<void>((_, reject) => (fail = reject)),
      abort: vi.fn(),
    })
    const store = useTransfersStore()

    const done = store.startUpload('s1', 'big.bin', new File(['x'], 'big.bin'))
    fail(new UploadAbortedError())
    await done
    expect(store.uploads['s1']).toBeDefined()
    vi.advanceTimersByTime(1500)
    expect(store.uploads['s1']).toBeUndefined()
  })
})
