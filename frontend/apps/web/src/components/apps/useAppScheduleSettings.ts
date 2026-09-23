import { useProjectApp } from '@omnara/react'
import type { AppCronTriggerTarget } from '@omnara/sdk'
import { useState } from 'react'

import { appScheduleEditor } from './app-schedule-schema'

export function useAppScheduleSettings(orgId: string, projectId: string, appId: string) {
  const app = useProjectApp(orgId, projectId, appId)
  const [jsonDraft, setJsonDraft] = useState<string>()
  const schema = app.data?.capabilities.schedule?.input_schema
  return {
    app,
    jsonDraft,
    setJsonDraft,
    valid: (settings: AppCronTriggerTarget['settings']) =>
      schema !== undefined && appScheduleEditor(schema, settings, jsonDraft).valid,
  }
}
