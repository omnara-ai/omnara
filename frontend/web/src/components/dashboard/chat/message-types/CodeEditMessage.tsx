import { Fragment } from 'react'
import { Message } from '@/types/dashboard'
import { CodeEditEnvelope } from '@/types/messages'
import { StructuredMessageCollapsible } from './StructuredMessageCollapsible'

interface CodeEditMessageProps {
  message: Message
  envelope: CodeEditEnvelope
}

export function CodeEditMessage({ message: _message, envelope }: CodeEditMessageProps) {
  const edits = envelope?.payload?.edits ?? []

  if (!edits.length) {
    return null
  }

  return (
    <StructuredMessageCollapsible title="Code edits">
      <div className="space-y-6">
        {edits.map((edit, index) => (
          <Fragment key={`${edit.file_path}-${index}`}>
            <div className="space-y-3 rounded-lg border border-border-subtle bg-background-base/40 p-4">
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="font-medium text-text-primary">{edit.file_path}</span>
                {edit.language && (
                  <span className="rounded bg-surface-panel px-2 py-0.5 text-xs uppercase tracking-wide text-text-secondary">
                    {edit.language}
                  </span>
                )}
              </div>

              {typeof edit.old_content === 'string' && edit.old_content.length > 0 && (
                <div>
                  <p className="text-xs font-medium uppercase tracking-wide text-text-secondary mb-1">
                    Before
                  </p>
                  <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-background-base p-3 text-sm text-text-secondary">
                    {edit.old_content}
                  </pre>
                </div>
              )}

              {typeof edit.new_content === 'string' && edit.new_content.length > 0 && (
                <div>
                  <p className="text-xs font-medium uppercase tracking-wide text-text-secondary mb-1">
                    After
                  </p>
                  <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-background-emphasis/60 p-3 text-sm text-text-primary">
                    {edit.new_content}
                  </pre>
                </div>
              )}

              {edit.old_content === null && typeof edit.new_content === 'string' && (
                <p className="text-xs text-text-secondary">This file content was created.</p>
              )}

              {edit.new_content === null && typeof edit.old_content === 'string' && (
                <p className="text-xs text-text-secondary">This content was removed.</p>
              )}
            </div>
          </Fragment>
        ))}
      </div>
    </StructuredMessageCollapsible>
  )
}
