import type { ConfiguredModelSummary, MachinePoolSummary, ToolCatalog } from '@omnara/sdk'

import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import { type BasicConfig, newMachineSource } from '@/components/agents/useAgentBuilderForm'

export interface AgentTemplate {
  id: string
  name: string
  description: string
  instruction: string
}

const defaultAgentToolNames = ['web_search', 'web_fetch'] as const

const templateToolNames = [
  'run_command',
  'write_process',
  'read_process',
  'stop_process',
  'list_processes',
  'list_machines',
  'inspect_machine',
  'ask_question',
  'web_search',
  'web_fetch',
  'upload_artifact',
  'download_artifact',
] as const

const generalAgent: AgentTemplate = {
  id: 'general',
  name: 'General agent',
  description: 'Researches, writes code, runs commands, and uses tools to finish tasks end to end.',
  instruction:
    "You are a general-purpose agent that can research, write code, run commands, and use connected tools to complete the user's task end to end.",
}

const deepResearcher: AgentTemplate = {
  id: 'deep-researcher',
  name: 'Deep researcher',
  description: 'Decomposes a question, reads sources in full, and writes a cited report.',
  instruction: `You are a research agent. Given a question or topic:

1. Decompose it into 3-5 concrete sub-questions that, answered together, cover the topic.
2. For each sub-question, run targeted web searches and fetch the most authoritative sources (prefer primary sources, official docs, peer-reviewed work over blog posts and aggregators).
3. Read the sources in full — don't skim. Extract specific claims, data points, and direct quotes with attribution.
4. Synthesize a report that answers the original question. Structure it by sub-question, cite every non-obvious claim inline, and close with a "confidence & gaps" section noting where sources disagreed or where you couldn't find good coverage.

Be skeptical. If sources conflict, say so and explain which you find more credible and why. Don't paper over uncertainty with confident-sounding prose.`,
}

const structuredExtractor: AgentTemplate = {
  id: 'structured-extractor',
  name: 'Structured extractor',
  description: 'Turns unstructured text into schema-valid JSON.',
  instruction: `You extract structured data from unstructured text. Given raw input (emails, PDFs, logs, transcripts, scraped HTML) and a target JSON schema:

1. Read the schema first. Note required vs optional fields, enums, and format constraints (dates, currencies, IDs). The schema is the contract — never emit a key it doesn't define.
2. Scan the input for each field. Prefer explicit values over inferred ones. If a required field is genuinely absent, use null rather than guessing.
3. Normalize as you extract: trim whitespace, coerce dates to ISO 8601, strip currency symbols into numeric + code, collapse enum synonyms to their canonical value.
4. Emit a single JSON object (or array, if the schema is a list) that validates against the schema. No prose, no markdown fences — just the JSON.

When the input is ambiguous, pick the most conservative interpretation and note the ambiguity in a top-level "_extraction_notes" field only if the schema allows additionalProperties.`,
}

export const agentTemplates = [generalAgent, deepResearcher, structuredExtractor]

export const generalAgentTemplateId = generalAgent.id

export function agentTemplateConfig(
  template: AgentTemplate,
  catalog?: ToolCatalog,
  defaultPool?: MachinePoolSummary,
  defaultModel?: ConfiguredModelSummary,
): Pick<BasicConfig, 'instruction' | 'tools' | 'machineSources' | 'providerConfig' | 'modelName'> {
  return {
    instruction: template.instruction,
    providerConfig: defaultModel?.provider_config ?? '',
    modelName: defaultModel?.name ?? '',
    tools: catalogTools(catalog, templateToolNames),
    machineSources: defaultPool
      ? [
          {
            ...newMachineSource('pool'),
            name: defaultPool.name,
            provider: defaultPool.provider,
            managementKind: defaultPool.management_kind,
          },
        ]
      : [],
  }
}

export function agentTemplateBasicConfig(
  template: AgentTemplate,
  catalog?: ToolCatalog,
  defaultPool?: MachinePoolSummary,
  defaultModel?: ConfiguredModelSummary,
): BasicConfig {
  return {
    mcpServers: [],
    skillIds: [],
    subagents: [],
    maxSubagents: '',
    ...agentTemplateConfig(template, catalog, defaultPool, defaultModel),
  }
}

export function agentTemplateName(currentName: string, template: AgentTemplate) {
  const trimmed = currentName.trim()
  const isTemplateName = agentTemplates.some((candidate) => candidate.name === trimmed)
  return trimmed === '' || isTemplateName ? template.name : currentName
}

export function defaultAgentTools(catalog?: ToolCatalog): BasicTool[] {
  return catalogTools(catalog, defaultAgentToolNames)
}

function catalogTools(catalog: ToolCatalog | undefined, names: readonly string[]): BasicTool[] {
  if (!catalog) return []
  const entries = new Map(catalog.built_in_tools.map((entry) => [entry.name, entry]))
  const tools: BasicTool[] = []
  for (const name of names) {
    const entry = entries.get(name)
    if (entry == null) continue
    tools.push({ name, permission: structuredClone(entry.default_permission) })
  }
  return tools
}
