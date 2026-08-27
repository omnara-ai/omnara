import type { ErrorIssue } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { AgentConfigIssueList } from '@/components/agents/AgentConfigIssueList'
import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import type { AgentConfigEditorState } from '@/components/agents/useAgentConfigEditor'

export function AgentConfigEditorFields({
  editor,
  orgId,
  projectId,
  header,
  yamlFieldId,
  yamlFieldClassName,
  issues,
}: {
  editor: AgentConfigEditorState
  orgId: string
  projectId: string
  header?: ReactNode
  yamlFieldId: string
  yamlFieldClassName: string
  issues?: readonly ErrorIssue[]
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
              onValueChange={editor.switchMode}
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
          <AgentConfigModelField
            orgId={orgId}
            projectId={projectId}
            value={editor.form.model}
            onChange={editor.form.setModel}
            onUnavailableChange={editor.form.reportModelUnavailable}
          />
          <AgentConfigBasicForm orgId={orgId} projectId={projectId} form={editor.form} />
          <AgentConfigIssueList issues={issues ?? []} />
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
          issues={issues}
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
