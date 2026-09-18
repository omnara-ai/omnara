import type { ToolPermissionMode } from '@omnara/sdk'
import type { ComponentType, SVGProps } from 'react'

import {
  CheckCircleIcon,
  CircleHelp,
  CircleIcon,
  HandRaisedIcon,
  NoSymbolIcon,
  XCircleIcon,
} from '@/components/icons'

type Icon = ComponentType<SVGProps<SVGSVGElement>>

export interface PermissionModeOption {
  value: string
  label: string
  description?: string
  icon: Icon
}

const modeIcons = new Map<string, Icon>([
  ['always_allow', CheckCircleIcon],
  ['always_ask', HandRaisedIcon],
  ['always_deny', XCircleIcon],
])

const hiddenMode = 'always_deny'

export function permissionModeOptions(
  modes: readonly ToolPermissionMode[] | undefined,
  current?: string,
): PermissionModeOption[] {
  return (modes ?? [])
    .filter((mode) => mode.name !== hiddenMode || mode.name === current)
    .map((mode) => ({
      value: mode.name,
      label: mode.label,
      description: mode.description,
      icon: modeIcons.get(mode.name) ?? CircleHelp,
    }))
}

export const inheritPermissionOption: PermissionModeOption = {
  value: 'inherit',
  label: 'Inherit',
  description: 'Follow the default permission.',
  icon: CircleIcon,
}

export const disabledPermissionOption: PermissionModeOption = {
  value: 'disabled',
  label: 'Disabled',
  description: 'Hide this tool from the agent.',
  icon: NoSymbolIcon,
}
