import type { SessionInfo } from '@discordjs/ws'
import { describe, expect, it, vi } from 'vitest'

import { discordAPIURL } from './bootstrap'
import { DiscordCheckpoint } from './checkpoint'
import { savedCheckpoint, session } from './runtime-test-support'

function setup(saved: SessionInfo | null = session) {
  const persist = vi.fn()
  const checkpoint = new DiscordCheckpoint(
    { shard_id: 0, shard_count: 1 },
    {
      checkpoint_version: 1,
      checkpoint: savedCheckpoint(saved),
    },
    discordAPIURL(),
    persist,
  )
  return { checkpoint, persist }
}

describe('Discord captured prefix', () => {
  it('never resumes from SDK observation ahead of capture', () => {
    const { checkpoint, persist } = setup()
    checkpoint.observe(0, { ...session, sequence: 3 })
    expect(checkpoint.retrieve(0)?.sequence).toBe(1)
    expect(persist).not.toHaveBeenCalled()
    checkpoint.captured(checkpoint.position(2))
    expect(checkpoint.retrieve(0)?.sequence).toBe(2)
    expect(persist).toHaveBeenLastCalledWith({
      version: 1,
      checkpoint: savedCheckpoint({ ...session, sequence: 2 }),
    })
  })
  it('returns copies and never moves a captured prefix backwards on replay', () => {
    const { checkpoint } = setup()
    const retrieved = checkpoint.retrieve(0)
    if (retrieved) retrieved.sequence = 900
    checkpoint.captured(checkpoint.position(0))
    expect(checkpoint.retrieve(0)?.sequence).toBe(1)
  })
  it('does not let a blocked old session overwrite genuine invalidation or a new session', () => {
    const { checkpoint, persist } = setup()
    const old = checkpoint.position(2)
    checkpoint.observe(0, null)
    checkpoint.observe(0, { ...session, sessionId: 'new-session' })
    checkpoint.captured(old)
    expect(checkpoint.retrieve(0)).toBeNull()
    expect(persist).toHaveBeenLastCalledWith({ version: 1, checkpoint: savedCheckpoint(null) })
    checkpoint.captured(checkpoint.position(1))
    expect(checkpoint.retrieve(0)?.sessionId).toBe('new-session')
  })
  it('keeps the captured prefix when intentional shutdown invokes the SDK null update', () => {
    const { checkpoint, persist } = setup()
    checkpoint.stop()
    checkpoint.observe(0, null)
    expect(checkpoint.retrieve(0)).toEqual(session)
    expect(persist).not.toHaveBeenCalled()
  })
  it('discards a saved session from a different shard count and rejects substituted URLs', () => {
    expect(setup({ ...session, shardCount: 2 }).checkpoint.retrieve(0)).toBeNull()
    expect(setup({ ...session, shardCount: 2 }).checkpoint.initialReadySeen).toBe(false)
    expect(() => setup({ ...session, resumeURL: 'wss://other.example' })).toThrow(
      'invalid_gateway_url',
    )
  })

  it('retains validated first READY across invalidation and a fresh checkpoint instance', () => {
    const persist = vi.fn()
    const checkpoint = new DiscordCheckpoint(
      { shard_id: 0, shard_count: 1 },
      {
        checkpoint_version: 1,
        checkpoint: {},
      },
      discordAPIURL(),
      persist,
    )
    checkpoint.observe(0, session)
    expect(checkpoint.initialReadySeen).toBe(false)
    checkpoint.captured(checkpoint.position(1), true)
    expect(checkpoint.initialReadySeen).toBe(true)
    checkpoint.observe(0, null)
    expect(persist).toHaveBeenLastCalledWith({ version: 1, checkpoint: savedCheckpoint(null) })
    const restarted = setup(null).checkpoint
    expect(restarted.retrieve(0)).toBeNull()
    expect(restarted.initialReadySeen).toBe(true)
  })

  it('cannot establish READY from SDK observation, a stale epoch or a callback after stop', () => {
    const checkpoint = new DiscordCheckpoint(
      { shard_id: 0, shard_count: 1 },
      {
        checkpoint_version: 1,
        checkpoint: {},
      },
      discordAPIURL(),
      vi.fn(),
    )
    checkpoint.observe(0, session)
    const old = checkpoint.position(1)
    checkpoint.observe(0, null)
    checkpoint.captured(old, true)
    expect(checkpoint.initialReadySeen).toBe(false)
    checkpoint.observe(0, { ...session, sessionId: 'replacement' })
    const next = checkpoint.position(1)
    checkpoint.stop()
    checkpoint.captured(next, true)
    expect(checkpoint.initialReadySeen).toBe(false)
  })
})
