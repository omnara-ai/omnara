import { useMachinePoolConnectionCheck } from '@omnara/react'
import { ApiError, type MachinePool, schemas } from '@omnara/sdk'

import { useDebouncedValue } from '@/hooks/use-resource-list'
import { errorMessage } from '@/lib/submit-status'

import {
  machinePoolConnectionCheckRequest,
  type MachinePoolFormMode,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import { type MachinePoolFieldErrors, machinePoolFieldErrors } from './machinePoolValidation'

export type MachinePoolConnectionState =
  | { state: 'idle' }
  | { state: 'checking' }
  | { state: 'connected' }
  | { state: 'failed'; message: string; details?: string }

export function useMachinePoolConnectionState(
  orgId: string,
  enabled: boolean,
  mode: MachinePoolFormMode,
  values: MachinePoolFormValues | null,
  savedProviderConfig?: MachinePool['provider_config'],
): MachinePoolConnectionState {
  const request =
    enabled &&
    mode !== 'cluster-edit' &&
    values !== null &&
    connectionFieldsValid(machinePoolFieldErrors(values, mode))
      ? machinePoolConnectionCheckRequest(values, savedProviderConfig)
      : null
  const requestKey = JSON.stringify(request)
  const checkedKey = useDebouncedValue(requestKey, 1000)
  const check = useMachinePoolConnectionCheck(orgId, parseConnectionCheckRequest(checkedKey))
  if (request === null) return { state: 'idle' }
  if (checkedKey !== requestKey || check.isLoading) return { state: 'checking' }
  if (check.isSuccess) return { state: 'connected' }
  if (!check.isError) return { state: 'idle' }
  const failure =
    check.error instanceof ApiError
      ? schemas.zMachinePoolConnectionCheckError.safeParse(check.error.body).data
      : undefined
  return {
    state: 'failed',
    message: errorMessage(check.error, 'Could not check the connection'),
    details: failure?.details,
  }
}

function connectionFieldsValid(errors: MachinePoolFieldErrors) {
  return (
    errors.secretId === undefined &&
    errors.providerScope === undefined &&
    errors.location === undefined
  )
}

function parseConnectionCheckRequest(key: string) {
  const parsed = schemas.zCheckMachinePoolConnectionRequest.safeParse(JSON.parse(key))
  return parsed.success ? parsed.data : null
}
