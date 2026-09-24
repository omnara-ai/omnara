import { useProjectIntegration } from '@omnara/react'
import type { IntegrationCronTriggerTarget } from '@omnara/sdk'
import { useState } from 'react'

import { integrationScheduleEditor } from './integration-schedule-schema'

export function useIntegrationScheduleSettings(
  orgId: string,
  projectId: string,
  integrationId: string,
) {
  const integration = useProjectIntegration(orgId, projectId, integrationId)
  const [jsonDraft, setJsonDraft] = useState<string>()
  const schema = integration.data?.capabilities.schedule?.input_schema
  return {
    integration,
    jsonDraft,
    setJsonDraft,
    valid: (settings: IntegrationCronTriggerTarget['settings']) =>
      schema !== undefined && integrationScheduleEditor(schema, settings, jsonDraft).valid,
  }
}
