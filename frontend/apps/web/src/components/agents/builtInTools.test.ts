import { describe, expect, it } from 'vitest'

import {
  addMachineToolsForNewSourceSelection,
  addMissingMachineTools,
  hasMissingMachineTools,
  recommendedMachineToolNames,
} from './builtInTools'

describe('machine tool completeness', () => {
  it('detects and adds only missing machine tools', () => {
    const runCommand = {
      name: 'run_command',
      permission: { mode: 'always_ask', parameters: {} },
    }
    const partial = [runCommand]
    const complete = addMissingMachineTools(partial)

    expect(hasMissingMachineTools(partial)).toBe(true)
    expect(complete[0]).toBe(runCommand)
    expect(complete.map((tool) => tool.name)).toEqual(recommendedMachineToolNames)
    expect(hasMissingMachineTools(complete)).toBe(false)
    expect(addMissingMachineTools(complete)).toBe(complete)
  })
})

describe('addMachineToolsForNewSourceSelection', () => {
  it('adds missing machine tools when a blank source is selected', () => {
    const tools = [{ name: 'ask_question', permission: null }]
    const result = addMachineToolsForNewSourceSelection(
      [{ id: 'source-1', name: '' }],
      [{ id: 'source-1', name: 'build-pool' }],
      tools,
    )

    expect(result).toEqual([
      ...tools,
      ...recommendedMachineToolNames.map((name) => ({ name, permission: null })),
    ])
    expect(result).toContainEqual({ name: 'upload_artifact', permission: null })
  })

  it('preserves existing tools and permissions without duplicates', () => {
    const runCommand = {
      name: 'run_command',
      permission: { mode: 'always_ask', parameters: {} },
    }
    const result = addMachineToolsForNewSourceSelection(
      [],
      [{ id: 'source-1', name: 'build-box' }],
      [runCommand],
    )

    expect(result[0]).toBe(runCommand)
    expect(result.map((tool) => tool.name)).toEqual(recommendedMachineToolNames)
  })

  it('does not add tools for empty rows or edits to selected sources', () => {
    const tools = [{ name: 'ask_question', permission: null }]

    expect(addMachineToolsForNewSourceSelection([], [{ id: 'source-1', name: '' }], tools)).toBe(
      tools,
    )
    expect(
      addMachineToolsForNewSourceSelection(
        [{ id: 'source-1', name: 'old-pool' }],
        [{ id: 'source-1', name: 'new-pool' }],
        tools,
      ),
    ).toBe(tools)
  })

  it('adds tools when granting another machine from a selected row', () => {
    const tools = [{ name: 'ask_question', permission: null }]
    const result = addMachineToolsForNewSourceSelection(
      [{ id: 'source-1', name: 'existing-machine' }],
      [
        { id: 'source-1', name: 'existing-machine' },
        { id: 'source-2', name: 'granted-machine' },
      ],
      tools,
    )

    expect(result).not.toBe(tools)
    expect(result.map((tool) => tool.name)).toEqual([
      'ask_question',
      ...recommendedMachineToolNames,
    ])
  })
})
