import type { ReactNode } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import type { AgentConfigEditorState } from '@/components/agents/useAgentConfigEditor'

/** The mode tabs, builder form, YAML field, and discard dialog both config
 *  editors render; the surrounding form, messages, and footer stay theirs. */
export function AgentConfigEditorFields({
  editor,
  orgId,
  projectId,
  header,
  yamlFieldId,
  yamlFieldClassName,
}: {
  editor: AgentConfigEditorState
  orgId: string
  projectId: string
  header?: ReactNode
  yamlFieldId: string
  yamlFieldClassName: string
}) {
  const { builderSession, canManage, dispatchMode, mode, showBuilder, source } = editor
  return (
    <>
      {(header != null || builderSession != null) && (
        <div
          className={
            header != null ? 'flex items-center justify-between gap-3' : 'flex justify-end'
          }
        >
          {header}
          {builderSession != null && (
            <PillTabs
              value={mode.mode}
              onValueChange={(nextMode) => {
                dispatchMode({ type: 'switch-mode', mode: nextMode })
              }}
              tabs={[
                { value: 'builder', label: 'Builder' },
                { value: 'yaml', label: 'YAML' },
              ]}
            />
          )}
        </div>
      )}
      {builderSession != null && (
        <div className={showBuilder ? 'flex flex-col gap-8' : 'hidden'}>
          <AgentConfigBasicForm
            orgId={orgId}
            projectId={projectId}
            session={builderSession}
            onYamlChange={editor.handleBuilderYamlChange}
          />
        </div>
      )}
      {!showBuilder && (
        <AgentConfigYamlField
          id={yamlFieldId}
          value={editor.editorYaml}
          onChange={(value) => {
            dispatchMode({
              type: 'editor-yaml-changed',
              yaml: value,
              builderYaml: editor.builderYaml,
            })
          }}
          readOnly={!canManage}
          className={yamlFieldClassName}
        />
      )}
      {canManage && builderSession == null && source !== '' && (
        <p className="text-muted-foreground text-sm">
          This configuration can’t be shown in the builder, so it’s editable as YAML only.
        </p>
      )}
      <ConfirmDiscardYamlDialog
        open={mode.confirmDiscard}
        onOpenChange={(open) => {
          dispatchMode({ type: 'set-confirm-discard', open })
        }}
        onConfirm={() => {
          dispatchMode({ type: 'discard-yaml-edits' })
        }}
      />
    </>
  )
}
