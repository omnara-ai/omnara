import type { Agent } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { fakeId } from '@/test/fixtures'

import { activityFilterOptions, agentStatusLabel, statusLabels } from './agent-status'

const timestamp = '2026-01-01T00:00:00Z'

function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: fakeId('agt'),
    org_id: fakeId('org'),
    project_id: fakeId('proj'),
    state: 'active',
    name: 'demo',
    created_at: timestamp,
    updated_at: timestamp,
    ...overrides,
  }
}

describe('activityFilterOptions', () => {
  it('covers every activity state plus an any choice', () => {
    expect(activityFilterOptions.map((option) => option.value)).toEqual([
      'all',
      'running',
      'waiting_on_interaction',
      'idle',
      'archived',
    ])
    expect(activityFilterOptions.map((option) => option.label)).toEqual([
      'Any status',
      statusLabels.running,
      statusLabels.waiting_on_interaction,
      statusLabels.idle,
      statusLabels.archived,
    ])
  })
})

describe('agentStatusLabel', () => {
  it('uses the activity state when present', () => {
    expect(
      agentStatusLabel(
        agent({
          activity: {
            state: 'waiting_on_interaction',
            last_activity_at: timestamp,
            open_interactions: { question_count: 1, permission_request_count: 0 },
            running_processes: { count: 0, process_ids: [] },
            working_subagents: { count: 0, agent_ids: [] },
          },
        }),
      ),
    ).toBe('Waiting')
  })

  it('falls back to archived when activity is omitted', () => {
    expect(agentStatusLabel(agent({ state: 'archived' }))).toBe('Archived')
  })
})
