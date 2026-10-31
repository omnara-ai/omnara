import { describe, expect, it } from 'vitest'

import { describeTool, summarizeActivity, type ToolInput } from './toolLabels'

describe('describeTool', () => {
  it('describes built-in tools from their input', () => {
    expect(describeTool('storage_read', { path: 'notes/todo.md' }, 'done')).toEqual({
      verb: 'Read',
      target: 'notes/todo.md',
    })
    expect(describeTool('web_search', { query: 'omnara pricing' }, 'running')).toEqual({
      verb: 'Searching',
      target: 'the web for "omnara pricing"',
    })
    expect(describeTool('run_command', { command: 'npm test' }, 'failed')).toEqual({
      verb: "Couldn't run",
      target: 'npm test',
    })
  })

  it('says when a call was denied or canceled instead of describing it as done', () => {
    expect(describeTool('run_command', { command: 'rm -rf build' }, 'denied').verb).toBe(
      "Wasn't allowed to run",
    )
    expect(describeTool('storage_read', { path: 'a.md' }, 'canceled')).toEqual({
      verb: "Didn't read",
      target: 'a.md',
    })
  })

  it('keeps long and multi-line commands to one line', () => {
    expect(describeTool('run_command', { command: 'cd app\nnpm test' }, 'done').target).toBe(
      'cd app…',
    )
  })

  it('names MCP and unknown tools by their humanized name', () => {
    expect(describeTool('mcp__linear__create_issue', {}, 'done')).toEqual({
      verb: 'Used',
      target: 'create issue on linear',
    })
    expect(describeTool('slack_post_message', {}, 'done').target).toBe('slack post message')
  })
})

describe('summarizeActivity', () => {
  const done = (name: string, input: ToolInput = {}) => ({
    kind: 'tool' as const,
    name,
    input,
    status: 'done' as const,
  })

  it('counts repeated calls of a countable tool', () => {
    expect(
      summarizeActivity([
        done('run_command', { command: 'ls' }),
        done('run_command', { command: 'pwd' }),
        done('run_command', { command: 'npm test' }),
      ]),
    ).toBe('Ran 3 commands')
  })

  it('reads a single call as its own phrase', () => {
    expect(summarizeActivity([done('run_command', { command: 'npm test' })])).toBe('Ran npm test')
  })

  it('joins up to three phrases before counting the rest', () => {
    expect(
      summarizeActivity([
        done('list_machines'),
        done('storage_read', { path: 'a.md' }),
        done('storage_read', { path: 'b.md' }),
        done('web_search', { query: 'x' }),
        done('skill', { name: 'deploy' }),
      ]),
    ).toBe('Listed available machines, read 2 files, searched the web for "x" and 1 more')
  })

  it('reads reasoning once, in order with the calls around it', () => {
    expect(
      summarizeActivity([
        { kind: 'reasoning', status: 'done' },
        done('run_command', { command: 'ls' }),
        { kind: 'reasoning', status: 'done' },
        done('run_command', { command: 'npm test' }),
      ]),
    ).toBe('Reasoned, ran 2 commands')
    expect(
      summarizeActivity([
        done('storage_read', { path: 'a.md' }),
        { kind: 'reasoning', status: 'running' },
      ]),
    ).toBe('Read a.md, reasoning')
  })

  it('reads reasoning on its own', () => {
    expect(summarizeActivity([{ kind: 'reasoning', status: 'done' }])).toBe('Reasoned')
  })
})
