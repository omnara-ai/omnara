import { useCreateMachinePool, useGrantMachinePoolToProject, useSecret } from '@omnara/react'
import type { MachinePool } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { OverridesCollapsible } from '@/components/machines/MachineOverrideFields'
import { ProjectShareChips } from '@/components/projects/ProjectShareChips'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FieldGroup } from '@/components/ui/field'
import { collectGrantFailures, type RetryGrantsPhase } from '@/lib/grant-failures'
import { errorMessage } from '@/lib/submit-status'

import { MachinePoolEnvironmentFields, MachinePoolLimitFields } from './MachinePoolAdvancedSection'
import { MachinePoolCapacityFields } from './MachinePoolCapacityFields'
import { MachinePoolCreateStep, type MachinePoolCreateStepStatus } from './MachinePoolCreateStep'
import {
  type MachinePoolCreateStep as MachinePoolCreateStepId,
  machinePoolCreateSteps,
  machinePoolCreateStepValid,
  machinePoolMachineSizeLabel,
} from './machinePoolCreateSteps'
import {
  machinePoolCreateRequest,
  machinePoolFormDefaults,
  machinePoolFormValid,
  type MachinePoolFormValues,
  machinePoolProviderLabel,
} from './MachinePoolDialogState'
import {
  MachinePoolCredentialField,
  MachinePoolDescriptionField,
  type MachinePoolFormSetValue,
  MachinePoolImageField,
  MachinePoolNameField,
  MachinePoolProviderField,
  MachinePoolScopeField,
  MachinePoolStartupScriptField,
} from './MachinePoolFields'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

const stepTitles: Record<MachinePoolCreateStepId, string> = {
  provider: 'Provider',
  image: 'Image',
  capacity: 'Capacity',
  environment: 'Environment',
}

const lastStep = machinePoolCreateSteps.length - 1

const stepFieldSelector =
  '[aria-current="step"] :is(input:not([type="hidden"]), textarea, button[role="combobox"])'

export function CreateMachinePoolDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const createMachinePool = useCreateMachinePool(orgId)
  const grantMachinePool = useGrantMachinePoolToProject(orgId)
  const [phase, setPhase] = useState<RetryGrantsPhase<MachinePool>>({ kind: 'form', error: '' })
  const [values, setValues] = useState<MachinePoolFormValues>(machinePoolFormDefaults)
  const [submitting, setSubmitting] = useState(false)
  const [current, setCurrent] = useState(0)
  const [reached, setReached] = useState(0)
  // The current step's card can be folded away without leaving the step.
  const [collapsed, setCollapsed] = useState(false)
  const stepsRef = useRef<HTMLDivElement>(null)
  const focusStep = useRef(false)
  const secret = useSecret(orgId, values.secretId, { enabled: open })
  const valid = machinePoolFormValid(values)
  const currentStep = machinePoolCreateSteps[current] ?? 'provider'
  const isLast = current === lastStep

  useEffect(() => {
    if (!focusStep.current) return
    focusStep.current = false
    stepsRef.current?.querySelector<HTMLElement>(stepFieldSelector)?.focus()
  }, [current])

  const setValue: MachinePoolFormSetValue = (key, value) => {
    setValues((previous) => ({ ...previous, [key]: value }))
  }

  function goToStep(index: number) {
    focusStep.current = true
    setCollapsed(false)
    setCurrent(index)
    setReached((previous) => Math.max(previous, index))
  }

  function stepStatus(index: number): MachinePoolCreateStepStatus {
    if (index === current) return 'active'
    const step = machinePoolCreateSteps[index]
    return step && index <= reached && machinePoolCreateStepValid(step, values)
      ? 'done'
      : 'upcoming'
  }

  async function create() {
    if (submitting || (phase.kind === 'form' && !valid)) return
    setSubmitting(true)
    setPhase((prev) => ({ ...prev, error: '' }))
    try {
      let pool = phase.kind === 'retry-grants' ? phase.created : null
      pool ??= await createMachinePool.mutateAsync(machinePoolCreateRequest(values))
      const grantResults = await Promise.allSettled(
        values.projectGrantIds.map((projectID) =>
          grantMachinePool.mutateAsync({ projectID, machine_pool_id: pool.id }),
        ),
      )
      const failures = collectGrantFailures(values.projectGrantIds, grantResults)
      if (failures) {
        setValue('projectGrantIds', failures.failedProjectIds)
        setPhase({
          kind: 'retry-grants',
          created: pool,
          error: `The pool was created, but ${failures.message}`,
        })
        return
      }
      // Keep the machine sizing so consecutive pools reuse it.
      setValues({
        ...machinePoolFormDefaults,
        cpu: values.cpu,
        memoryGb: values.memoryGb,
        maxMachines: values.maxMachines,
      })
      setPhase({ kind: 'form', error: '' })
      setCurrent(0)
      setCollapsed(false)
      setReached(0)
      onOpenChange(false)
    } catch (err) {
      setPhase((prev) => ({ ...prev, error: errorMessage(err, 'Could not create pool') }))
    } finally {
      setSubmitting(false)
    }
  }

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (phase.kind === 'retry-grants' || isLast) {
      void create()
      return
    }
    if (machinePoolCreateStepValid(currentStep, values)) goToStep(current + 1)
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setPhase({ kind: 'form', error: '' })
      setCurrent(0)
      setCollapsed(false)
    }
    onOpenChange(nextOpen)
  }

  const summaries: Record<MachinePoolCreateStepId, ReactNode> = {
    provider: (
      <>
        {values.name} · {machinePoolProviderLabel(values.provider)}
        {secret.data && (
          <>
            {' · '}
            <span className="font-mono">{secret.data.name}</span>
          </>
        )}
      </>
    ),
    image: (
      <>
        {values.image.trim() ? (
          <span className="font-mono">{values.image.trim()}</span>
        ) : (
          `Default ${machinePoolProviderDefinitions[values.provider].resource.label.toLowerCase()}`
        )}
        {values.providerScope.trim() && ` · ${values.providerScope.trim()}`}
      </>
    ),
    capacity: (
      <>
        {values.location.trim() && `${values.location.trim()} · `}
        up to {values.maxMachines} × {machinePoolMachineSizeLabel(values)}
      </>
    ),
    environment: environmentSummary(values),
  }

  const stepFields: Record<MachinePoolCreateStepId, ReactNode> = {
    provider: (
      <>
        <div className="grid gap-4 sm:grid-cols-2">
          <MachinePoolNameField values={values} setValue={setValue} />
          <MachinePoolProviderField values={values} setValue={setValue} />
        </div>
        <MachinePoolDescriptionField values={values} setValue={setValue} />
        <MachinePoolCredentialField
          orgId={orgId}
          enabled={open}
          values={values}
          setValue={setValue}
        />
      </>
    ),
    image: (
      <>
        <MachinePoolImageField values={values} setValue={setValue} />
        <MachinePoolScopeField values={values} setValue={setValue} />
      </>
    ),
    capacity: (
      <>
        <MachinePoolCapacityFields values={values} setValue={setValue} />
        <OverridesCollapsible title="Advanced">
          <FieldGroup>
            <MachinePoolLimitFields clusterManaged={false} values={values} setValue={setValue} />
          </FieldGroup>
        </OverridesCollapsible>
      </>
    ),
    environment: (
      <>
        <MachinePoolStartupScriptField label="Startup script" values={values} setValue={setValue} />
        <MachinePoolEnvironmentFields
          orgId={orgId}
          enabled={open}
          clusterManaged={false}
          values={values}
          setValue={setValue}
        />
      </>
    ),
  }

  const primaryLabel =
    phase.kind === 'retry-grants' ? 'Retry sharing' : isLast ? 'Create pool' : 'Continue'
  const primaryReady =
    phase.kind === 'retry-grants' ||
    (isLast ? valid : machinePoolCreateStepValid(currentStep, values))

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        className="max-h-[85svh] sm:max-w-2xl"
        onOpenAutoFocus={(event) => {
          const field = stepsRef.current?.querySelector<HTMLElement>(stepFieldSelector)
          if (!field) return
          event.preventDefault()
          field.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>New machine pool</DialogTitle>
          <DialogDescription>Pools provision the machines your agents run on.</DialogDescription>
        </DialogHeader>
        <form className="flex flex-col gap-4" onSubmit={submit}>
          <div ref={stepsRef} className="flex flex-col gap-3">
            {machinePoolCreateSteps.map((step, index) => (
              <MachinePoolCreateStep
                key={step}
                number={index + 1}
                title={stepTitles[step]}
                status={stepStatus(index)}
                summary={summaries[step]}
                open={index === current && !collapsed}
                optional={step === 'environment'}
                onOpenChange={(nextOpen) => {
                  if (index === current) setCollapsed(!nextOpen)
                  else if (nextOpen) goToStep(index)
                }}
              >
                {stepFields[step]}
              </MachinePoolCreateStep>
            ))}
          </div>
          {phase.error && (
            <p role="alert" className="text-destructive text-sm">
              {phase.error}
            </p>
          )}
          <div className="-mx-4 flex flex-wrap items-center gap-3 border-t px-4 pt-4 sm:-mx-6 sm:px-6">
            <ProjectShareChips
              orgId={orgId}
              value={values.projectGrantIds}
              onChange={(projectGrantIds) => {
                setValue('projectGrantIds', projectGrantIds)
              }}
              disabled={submitting}
              isProjectEligible={(project) => project.access.can_manage_access}
            />
            <div className="ml-auto flex items-center gap-3">
              <span className="text-muted-foreground whitespace-nowrap text-sm">
                Step {current + 1} of {machinePoolCreateSteps.length}
              </span>
              <div className="flex gap-2">
                {phase.kind === 'form' && !isLast && valid && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={submitting}
                    onClick={() => {
                      void create()
                    }}
                  >
                    Skip &amp; create
                  </Button>
                )}
                <Button type="submit" disabled={submitting || !primaryReady} loading={submitting}>
                  {primaryLabel}
                </Button>
              </div>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** What step 4 adds to each machine, e.g. "Startup script · 2 variables". */
function environmentSummary(values: MachinePoolFormValues) {
  const variables = values.envRows.length + values.secretEnvRows.length
  const parts = [
    values.startupScript.trim() && 'Startup script',
    values.cwd.trim(),
    variables > 0 && `${String(variables)} ${variables === 1 ? 'variable' : 'variables'}`,
  ].filter(Boolean)
  return parts.length > 0 ? parts.join(' · ') : 'Defaults'
}
