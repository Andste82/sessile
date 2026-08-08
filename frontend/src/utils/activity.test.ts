import { describe, expect, it } from 'vitest'
import type { Session } from '@/api/types'
import { activitySummary, displayDirectory, indicatorFor, indicatorLabel } from './activity'

function session(over: Partial<Session> = {}): Session {
  return {
    id: 'a',
    name: 'build',
    directory: 'project-a',
    shell: 'bash',
    status: 'running',
    pid: 42,
    created: '2026-08-07T10:00:00Z',
    lastActivity: '2026-08-07T10:05:00Z',
    rows: 24,
    cols: 80,
    clientCount: 1,
    activity: 'idle',
    activitySince: '2026-08-07T10:04:00Z',
    command: 'bash',
    cwd: 'project-a',
    ...over,
  }
}

describe('indicatorFor', () => {
  it.each([
    ['busy', 'busy'],
    ['waiting', 'waiting'],
    ['idle', 'idle'],
  ] as const)('maps a running session with activity %s', (activity, want) => {
    expect(indicatorFor('running', activity)).toBe(want)
  })

  // A stopped session's activity is cleared server-side, but the two fields
  // travel together and a stale list must not paint a dead session green.
  it('lets status win over a stale activity', () => {
    expect(indicatorFor('stopped', 'busy')).toBe('stopped')
    expect(indicatorFor('stopped', '')).toBe('stopped')
  })

  it('falls back to idle rather than rendering nothing', () => {
    expect(indicatorFor('running', '')).toBe('idle')
  })
})

describe('indicatorLabel', () => {
  // The label is the tooltip and the accessible name, and it has to say what
  // the dot is about: this one is the program in the session, not the socket.
  it('describes the state instead of naming it', () => {
    expect(indicatorLabel('waiting')).toBe('waiting for input')
    expect(indicatorLabel('busy')).toBe('working')
    expect(indicatorLabel('idle')).toBe('idle at the prompt')
    expect(indicatorLabel('stopped')).toBe('stopped')
  })
})

describe('activitySummary', () => {
  it('leads with the program, which is the measured part', () => {
    expect(activitySummary(session({ activity: 'waiting', command: 'claude' }))).toBe(
      'claude · waiting for input',
    )
  })

  it('names the shell when it is what is in the foreground', () => {
    expect(activitySummary(session({ activity: 'idle', command: 'bash' }))).toBe(
      'bash · idle at the prompt',
    )
  })

  // No /proc, or the lookup lost a race with an exiting process.
  it('still says something true with no program name', () => {
    expect(activitySummary(session({ activity: 'busy', command: '' }))).toBe('working')
  })

  it('says only stopped for a stopped session', () => {
    expect(activitySummary(session({ status: 'stopped', activity: '', command: '' }))).toBe(
      'stopped',
    )
  })
})

describe('displayDirectory', () => {
  it('prefers where the shell actually is', () => {
    expect(displayDirectory(session({ directory: 'project-a', cwd: 'project-a/backend' }))).toBe(
      'project-a/backend',
    )
  })

  // All that is left once the session stops, or outside Linux.
  it('falls back to where it was started', () => {
    expect(displayDirectory(session({ directory: 'project-a', cwd: '' }))).toBe('project-a')
  })
})
