import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from '@dnd-kit/core'
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import type { AgentInput } from '@omnara/sdk'
import { GripVertical, X } from 'lucide-react'
import { useState } from 'react'

function inputPreview(input: AgentInput) {
  const blocks = input.content_blocks?.filter((block) => block.metadata?.omnara_hidden !== 'true')
  const text = blocks
    ?.flatMap((block) =>
      block.type === 'text'
        ? [
            (typeof block.metadata?.omnara_display_text === 'string'
              ? block.metadata.omnara_display_text
              : block.text
            ).trim(),
          ]
        : [],
    )
    .filter(Boolean)
    .join('\n')
  if (text != null && text !== '') return text
  return blocks?.some((block) => block.type === 'media_ref') ? 'Attachment' : 'Message'
}

export function AgentInputQueue({
  inputs,
  canOperate,
  canSendNow,
  actionPending,
  onSendNow,
  onRemove,
  onMove,
}: {
  inputs: AgentInput[]
  canOperate: boolean
  canSendNow: boolean
  actionPending: boolean
  onSendNow: (inputID: string) => Promise<unknown>
  onRemove: (inputID: string) => Promise<unknown>
  onMove: (inputID: string, anchorInputID: string, position: 'before' | 'after') => Promise<unknown>
}) {
  const [orderedInputs, setOrderedInputs] = useState<AgentInput[] | null>(null)
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  if (inputs.length === 0) return null

  const visibleInputs = orderedInputs ?? inputs

  async function handleDragEnd({ active, over }: DragEndEvent) {
    if (over == null || active.id === over.id) return

    const oldIndex = visibleInputs.findIndex((input) => input.id === active.id)
    const newIndex = visibleInputs.findIndex((input) => input.id === over.id)
    if (oldIndex === -1 || newIndex === -1) return

    setOrderedInputs(arrayMove(visibleInputs, oldIndex, newIndex))
    await onMove(
      String(active.id),
      String(over.id),
      oldIndex < newIndex ? 'after' : 'before',
    ).finally(() => {
      setOrderedInputs(null)
    })
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragEnd={(event) => void handleDragEnd(event)}
    >
      <SortableContext
        items={visibleInputs.map((input) => input.id)}
        strategy={verticalListSortingStrategy}
      >
        <section
          aria-label="Queued messages"
          className="bg-muted/50 divide-border divide-y rounded-t-xl border border-b-0"
        >
          {visibleInputs.map((input, index) => (
            <SortableQueueRow
              key={input.id}
              input={input}
              index={index}
              reorderable={visibleInputs.length >= 2}
              canOperate={canOperate}
              canSendNow={canSendNow}
              actionPending={actionPending}
              onSendNow={onSendNow}
              onRemove={onRemove}
            />
          ))}
        </section>
      </SortableContext>
    </DndContext>
  )
}

function SortableQueueRow({
  input,
  index,
  reorderable,
  canOperate,
  canSendNow,
  actionPending,
  onSendNow,
  onRemove,
}: {
  input: AgentInput
  index: number
  reorderable: boolean
  canOperate: boolean
  canSendNow: boolean
  actionPending: boolean
  onSendNow: (inputID: string) => Promise<unknown>
  onRemove: (inputID: string) => Promise<unknown>
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: input.id,
    disabled: !reorderable || !canOperate || actionPending,
  })

  return (
    <div
      ref={setNodeRef}
      className={`relative flex min-w-0 items-center gap-2 px-3 py-2 ${isDragging ? 'bg-background shadow-sm' : ''}`}
      style={{
        transform: CSS.Transform.toString(transform && { ...transform, x: 0 }),
        transition,
        opacity: isDragging ? 0.7 : undefined,
        zIndex: isDragging ? 1 : undefined,
      }}
    >
      {reorderable && (
        <button
          type="button"
          aria-label={`Reorder queued message ${index + 1}`}
          title="Drag to reorder"
          disabled={!canOperate || actionPending}
          className="text-muted-foreground hover:text-foreground cursor-grab touch-none rounded p-1.5 transition-colors active:cursor-grabbing disabled:pointer-events-none disabled:opacity-50"
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-3.5" />
        </button>
      )}
      <span className="text-muted-foreground w-8 shrink-0 text-xs font-medium tabular-nums">
        {index === 0 ? 'Next' : index + 1}
      </span>
      <p className="text-foreground min-w-0 flex-1 truncate text-sm">{inputPreview(input)}</p>
      {canSendNow && (
        <button
          type="button"
          disabled={actionPending}
          className="text-muted-foreground hover:text-primary shrink-0 rounded px-1.5 py-1 text-xs font-medium transition-colors disabled:pointer-events-none disabled:opacity-50"
          onClick={() => void onSendNow(input.id)}
        >
          Send now
        </button>
      )}
      <button
        type="button"
        aria-label="Remove queued message"
        disabled={!canOperate || actionPending}
        className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive shrink-0 rounded p-1 transition-colors disabled:pointer-events-none disabled:opacity-50"
        onClick={() => void onRemove(input.id)}
      >
        <X className="size-3.5" />
      </button>
    </div>
  )
}
