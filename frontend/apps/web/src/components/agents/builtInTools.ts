import type { ToolPermissionSelection } from '@omnara/sdk'

export const machineExecutionTools = [
  'run_command',
  'write_process',
  'read_process',
  'stop_process',
  'list_processes',
  'list_machines',
  'inspect_machine',
] as const

export const integrationTools = ['send_integration_message', 'set_integration_target'] as const

export const builtInToolsets = [
  { name: 'Machine tools', tools: machineExecutionTools },
  { name: 'Integration tools', tools: integrationTools },
] as const

interface MachineSourceSelection {
  id: string
  name: string
}

interface ToolSelection {
  name: string
  permission: ToolPermissionSelection | null
}

export function hasMissingMachineTools(tools: ToolSelection[]): boolean {
  const selectedTools = new Set(tools.map((tool) => tool.name))
  return machineExecutionTools.some((name) => !selectedTools.has(name))
}

export function addMissingMachineTools(tools: ToolSelection[]): ToolSelection[] {
  const selectedTools = new Set(tools.map((tool) => tool.name))
  const additions: ToolSelection[] = []
  for (const name of machineExecutionTools) {
    if (!selectedTools.has(name)) additions.push({ name, permission: null })
  }
  return additions.length === 0 ? tools : [...tools, ...additions]
}

export function addMachineToolsForNewSourceSelection(
  currentSources: readonly MachineSourceSelection[],
  nextSources: readonly MachineSourceSelection[],
  tools: ToolSelection[],
): ToolSelection[] {
  const currentNames = new Map(currentSources.map((source) => [source.id, source.name.trim()]))
  const sourceSelected = nextSources.some(
    (source) => source.name.trim() !== '' && (currentNames.get(source.id) ?? '') === '',
  )
  if (!sourceSelected) return tools

  return addMissingMachineTools(tools)
}
