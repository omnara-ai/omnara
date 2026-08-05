export const machineExecutionTools = [
  'run_command',
  'write_process',
  'read_process',
  'stop_process',
  'list_processes',
] as const

export const integrationTools = ['send_integration_message', 'set_integration_target'] as const

export const builtInToolsets = [
  { name: 'Machine tools', tools: machineExecutionTools },
  { name: 'Integration tools', tools: integrationTools },
] as const
