
import { Button } from '@/components/ui/button'
import { InstanceDetail, AgentStatus } from '@/types/dashboard'
import { CheckCircle } from 'lucide-react'
import { formatAgentTypeName } from '@/utils/statusUtils'
import { QueueStatusBadge } from '../chat/QueueStatusBadge'

interface InstanceHeaderProps {
  instance: InstanceDetail
  isWaitingForInput: boolean
  onMarkCompleted?: () => void
  completingInstance?: boolean
  queueCount?: number
  onQueueClick?: () => void
}

function formatTime(dateString: string) {
  return new Date(dateString).toLocaleString()
}

export function InstanceHeader({ instance, isWaitingForInput, onMarkCompleted, completingInstance, queueCount = 0, onQueueClick }: InstanceHeaderProps) {
  // Single primary status indicator with stoplight colors
  const renderPrimaryStatus = () => {
    const ttlSeconds = 60
    const ts = instance.last_heartbeat_at ? new Date(instance.last_heartbeat_at).getTime() : null
    const secondsSince = ts ? Math.floor((Date.now() - ts) / 1000) : null
    const isOnline = secondsSince !== null && secondsSince <= ttlSeconds

    // Special-case: Completed sessions should always show "Completed" with a checkmark
    if (instance.status === AgentStatus.COMPLETED) {
      return (
        <div
          className="bg-surface-panel/80 border border-border-divider px-3.5 py-2 rounded-full flex items-center space-x-2 shadow-sm"
          title="This session has been completed."
        >
          <CheckCircle className="w-4 h-4 text-green-400" />
          <span className="text-sm font-semibold text-text-primary">Completed</span>
        </div>
      )
    }

    // Priority for non-completed: Offline > Waiting > Active (with additional states)
    let label = 'Active'
    let color = 'text-green-300'
    let dot = 'bg-green-400'
    let tooltip = 'The agent is connected and running.'

    if (!isOnline) {
      label = 'Offline'
      color = 'text-red-300'
      dot = 'bg-red-400'
      tooltip = 'Connection lost. Please check your internet and refresh.'
    } else if (instance.status === AgentStatus.AWAITING_INPUT) {
      label = 'Waiting'
      color = 'text-yellow-300'
      dot = 'bg-yellow-400'
      tooltip = 'The agent is waiting for your response.'
    } else if (instance.status === AgentStatus.PAUSED) {
      label = 'Paused'
      color = 'text-blue-300'
      dot = 'bg-blue-400'
      tooltip = 'This session is paused.'
    } else if (instance.status === AgentStatus.FAILED) {
      label = 'Failed'
      color = 'text-red-300'
      dot = 'bg-red-400'
      tooltip = 'The agent encountered an error.'
    } else if (instance.status === AgentStatus.KILLED) {
      label = 'Stopped'
      color = 'text-red-300'
      dot = 'bg-red-400'
      tooltip = 'This session was stopped by the user.'
    }

    return (
      <div
        className="bg-surface-panel/80 border border-border-divider px-3.5 py-2 rounded-full flex items-center space-x-2 shadow-sm"
        title={tooltip}
      >
        <span className={`inline-block w-2.5 h-2.5 rounded-full ${dot}`}></span>
        <span className={`text-sm font-semibold ${color}`}>{label}</span>
      </div>
    )
  }

  return (
    <div className="flex items-center justify-between">
      {/* Left side: Title */}
      <h2 className="text-3xl font-bold tracking-tight text-text-primary">
        {formatAgentTypeName(instance.agent_type_name || 'Agent')} Session
      </h2>
      
      {/* Right side: Actions and Primary Status */}
      <div className="flex items-center space-x-4">
        {onMarkCompleted && instance.status !== AgentStatus.COMPLETED && (
          <Button
            onClick={onMarkCompleted}
            disabled={completingInstance}
            className="bg-surface-panel text-text-primary hover:bg-interactive-hover transition-colors font-medium px-4 py-2 rounded-full border-0"
          >
            <CheckCircle className="h-4 w-4 mr-2 text-text-secondary" />
            {completingInstance ? 'Marking...' : 'Mark as Completed'}
          </Button>
        )}
        {queueCount > 0 && onQueueClick && (
          <QueueStatusBadge pendingCount={queueCount} onClick={onQueueClick} />
        )}
        {renderPrimaryStatus()}
      </div>
    </div>
  )
}
