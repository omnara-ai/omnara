import type { UseAgentChatResult } from '@omnara/react'
import { type KeyboardEvent, type SyntheticEvent, useState } from 'react'

import { SendHorizontal, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

export function AgentComposer({
  chat,
  onCancel,
  cancelPending,
  cancelError,
  canOperate,
  className,
}: {
  chat: UseAgentChatResult
  onCancel: () => Promise<unknown>
  cancelPending: boolean
  cancelError?: Error | null
  canOperate: boolean
  className?: string
}) {
  const [text, setText] = useState('')
  const working = chat.isWorking
  const canSend = canOperate && chat.historyStatus === 'success' && text.trim() !== ''

  async function send() {
    if (!canSend) return
    const content = text.trim()
    setText('')
    try {
      await chat.sendMessage({ text: content })
    } catch {
      setText(content)
    }
  }

  function onSubmit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    void send()
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault()
      void send()
    }
  }

  return (
    <form
      onSubmit={onSubmit}
      className={cn('bg-background rounded-xl border p-2 shadow-sm', className)}
    >
      {chat.error && <p className="text-destructive px-2 pb-2 text-xs">{chat.error.message}</p>}
      {cancelError && <p className="text-destructive px-2 pb-2 text-xs">{cancelError.message}</p>}
      <Textarea
        value={text}
        placeholder={
          !canOperate
            ? 'You don’t have permission to message this agent'
            : chat.historyStatus === 'pending'
              ? 'Loading conversation…'
              : 'Message the agent…'
        }
        className="max-h-40 min-h-16 resize-none border-0 bg-transparent px-2 shadow-none focus-visible:ring-0"
        disabled={!canOperate || chat.historyStatus !== 'success'}
        onChange={(event) => {
          setText(event.target.value)
        }}
        onKeyDown={onKeyDown}
      />
      <div className="flex items-center justify-between gap-3 px-1 pt-1">
        <p className="text-muted-foreground hidden text-[11px] sm:block">
          Enter to send · Shift + Enter for a new line
        </p>
        <div className="flex items-center gap-2">
          {working && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={!canOperate || cancelPending}
              loading={cancelPending}
              icon={<Square className="size-3 fill-current" />}
              onClick={() => void onCancel().catch(() => undefined)}
            >
              Stop agent
            </Button>
          )}
          <Button type="submit" size="sm" disabled={!canSend}>
            <SendHorizontal className="size-3.5" /> Send
          </Button>
        </div>
      </div>
    </form>
  )
}
