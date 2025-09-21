import { CheckCircle2, Circle, CircleDot } from 'lucide-react'
import { Message } from '@/types/dashboard'
import { TodoEnvelope } from '@/types/messages'
import { StructuredMessageCollapsible } from './StructuredMessageCollapsible'

interface TodoMessageProps {
  message: Message
  envelope: TodoEnvelope
}

const STATUS_CONFIG: Record<
  TodoEnvelope['payload']['todos'][number]['status'],
  { label: string; icon: typeof Circle }
> = {
  pending: { label: 'Pending', icon: Circle },
  in_progress: { label: 'In Progress', icon: CircleDot },
  completed: { label: 'Completed', icon: CheckCircle2 }
}

export function TodoMessage({ message: _message, envelope }: TodoMessageProps) {
  const todos = envelope?.payload?.todos ?? []

  if (!todos.length) {
    return null
  }

  return (
    <StructuredMessageCollapsible title="Todo list">
      <div className="space-y-3">
        {todos.map(todo => {
          const statusConfig = STATUS_CONFIG[todo.status]
          const Icon = statusConfig.icon
          const isCompleted = todo.status === 'completed'

          return (
            <div
              key={todo.id}
              className="flex items-start gap-3 rounded-lg border border-border-subtle bg-background-base/40 p-3"
            >
              <Icon
                className={`mt-0.5 h-4 w-4 ${
                  isCompleted ? 'text-emerald-400' : 'text-text-secondary'
                }`}
              />
              <div className="space-y-1">
                <p className={`text-sm ${isCompleted ? 'line-through text-text-secondary' : 'text-text-primary'}`}>
                  {todo.content}
                </p>
                <span className="inline-flex items-center rounded-full bg-surface-panel px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-text-secondary">
                  {statusConfig.label}
                </span>
              </div>
            </div>
          )
        })}
      </div>
    </StructuredMessageCollapsible>
  )
}
