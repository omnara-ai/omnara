import { useEffect, useState } from 'react'

import {
  type BasicConfig,
  type BasicMachineSource,
  type BasicMcpServer,
  emptyBasicConfig,
  isBasicConfigComplete,
} from '@/components/agents/agentConfigBasic'
import type { BasicConfigSession } from '@/components/agents/agentConfigBasicYaml'
import type { ModelSelection } from '@/components/agents/AgentConfigModelField'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'

export function useAgentBuilderForm(
  session: BasicConfigSession,
  onYamlChange: (yaml: string, blocked: boolean) => void,
) {
  const [draft, setDraft] = useState<BasicConfig>(session.initialDraft ?? emptyBasicConfig)
  const [unavailableSkillIds, setUnavailableSkillIds] = useState<string[]>([])
  const [unavailableSourceIds, setUnavailableSourceIds] = useState<string[]>([])
  const [modelUnavailable, setModelUnavailable] = useState(false)

  const blocked =
    unavailableSkillIds.length > 0 ||
    unavailableSourceIds.length > 0 ||
    modelUnavailable ||
    !isBasicConfigComplete(draft)

  useEffect(() => {
    onYamlChange(session.apply(draft), blocked)
  }, [blocked, draft, onYamlChange, session])

  const patch = (fields: Partial<BasicConfig>) => {
    setDraft((prev) => ({ ...prev, ...fields }))
  }

  return {
    instruction: draft.instruction,
    model: { providerConfig: draft.providerConfig, modelName: draft.modelName },
    machineSources: draft.machineSources,
    tools: draft.tools,
    skillIds: draft.skillIds,
    mcpServers: draft.mcpServers,
    setInstruction: (instruction: string) => {
      patch({ instruction })
    },
    setModel: (model: ModelSelection) => {
      patch({ providerConfig: model.providerConfig, modelName: model.modelName })
    },
    setMachineSources: (machineSources: BasicMachineSource[]) => {
      patch({ machineSources })
    },
    setTools: (tools: BasicTool[]) => {
      patch({ tools })
    },
    setSkillIds: (skillIds: string[]) => {
      patch({ skillIds })
    },
    setMcpServers: (mcpServers: BasicMcpServer[]) => {
      patch({ mcpServers })
    },
    reportModelUnavailable: setModelUnavailable,
    reportUnavailableSourceIds: setUnavailableSourceIds,
    reportUnavailableSkillIds: setUnavailableSkillIds,
  }
}
