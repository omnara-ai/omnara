import type { AgentConfig } from '@omnara/sdk'
import { isMap } from 'yaml'

import { parseSourceDocument } from '@/components/agents/agentConfigBasicExtract'

export function withReasoningEffort(
  source: string,
  format: NonNullable<AgentConfig['source_format']>,
  effort: string,
): string | null {
  const doc = parseSourceDocument(source)
  if (doc == null || !isMap(doc.get('model'))) return null
  const reasoning = doc.getIn(['model', 'reasoning'])
  if (reasoning != null && !isMap(reasoning)) return null
  doc.setIn(['model', 'reasoning', 'effort'], effort)
  return format === 'json' ? JSON.stringify(doc.toJS(), null, 2) : doc.toString()
}
