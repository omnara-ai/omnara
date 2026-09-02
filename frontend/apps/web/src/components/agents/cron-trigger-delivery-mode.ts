import { type CronTriggerDeliveryMode } from '@omnara/sdk'

export interface CronTriggerDeliveryModeOption {
  value: CronTriggerDeliveryMode
  label: string
  hint: string
}

export const cronTriggerDeliveryModeOptions: CronTriggerDeliveryModeOption[] = [
  {
    value: 'queued',
    label: 'Queue',
    hint: "Each firing waits in the agent's backlog until earlier inputs finish.",
  },
  {
    value: 'steering',
    label: 'Steer',
    hint: 'Each firing is delivered into the current turn, or starts a new turn if the agent is idle.',
  },
]

export function cronTriggerDeliveryModeLabel(mode: CronTriggerDeliveryMode) {
  return cronTriggerDeliveryModeOptions.find((option) => option.value === mode)?.label ?? mode
}

export function cronTriggerDeliveryModeHint(mode: CronTriggerDeliveryMode) {
  return cronTriggerDeliveryModeOptions.find((option) => option.value === mode)?.hint
}
