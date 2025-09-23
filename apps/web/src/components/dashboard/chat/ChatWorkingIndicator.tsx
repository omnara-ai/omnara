import { InstanceDetail as IInstanceDetail, AgentStatus } from '@/types/dashboard'
import { Loader2, Pause, AlertTriangle, Power } from 'lucide-react'

interface ChatWorkingIndicatorProps {
  instance: IInstanceDetail
}

export function ChatWorkingIndicator({ instance }: ChatWorkingIndicatorProps) {
  // Determine online status using heartbeat TTL (consistent with header)
  const ttlSeconds = 60
  const ts = instance.last_heartbeat_at ? new Date(instance.last_heartbeat_at).getTime() : null
  const secondsSince = ts ? Math.floor((Date.now() - ts) / 1000) : null
  const isOnline = secondsSince !== null && secondsSince <= ttlSeconds

  // Hide for completed sessions
  if (instance.status === AgentStatus.COMPLETED) return null

  // While waiting for input, we already render an inline prompt under the last message
  if (instance.status === AgentStatus.AWAITING_INPUT) return null

  // Offline indicator (highest priority)
  if (!isOnline) {
    return (
      <div className="bg-surface-panel/80 border border-border-divider rounded-lg px-3 py-2 flex items-center space-x-2 text-red-300">
        <span className="inline-block w-2.5 h-2.5 rounded-full bg-red-400" />
        <span className="text-sm font-medium">Agent is offline</span>
      </div>
    )
  }

  // Specific non-active states
  if (instance.status === AgentStatus.PAUSED) {
    return (
      <div className="bg-surface-panel/80 border border-border-divider rounded-lg px-3 py-2 flex items-center space-x-2 text-blue-300">
        <Pause className="w-4 h-4" />
        <span className="text-sm font-medium">Paused</span>
      </div>
    )
  }

  if (instance.status === AgentStatus.FAILED) {
    return (
      <div className="bg-surface-panel/80 border border-border-divider rounded-lg px-3 py-2 flex items-center space-x-2 text-red-300">
        <AlertTriangle className="w-4 h-4" />
        <span className="text-sm font-medium">Error encountered</span>
      </div>
    )
  }

  if (instance.status === AgentStatus.KILLED) {
    return (
      <div className="bg-surface-panel/80 border border-border-divider rounded-lg px-3 py-2 flex items-center space-x-2 text-red-300">
        <Power className="w-4 h-4" />
        <span className="text-sm font-medium">Stopped</span>
      </div>
    )
  }

  // Default active/working indicator
  return (
    <div className="bg-surface-panel/80 border border-border-divider rounded-lg px-3 py-2 flex items-center space-x-2 text-green-300">
      <Loader2 className="w-4 h-4 animate-spin" />
      <span className="text-sm font-medium">Agent is working…</span>
      {/* <span className="text-xs text-text-secondary">• Press Esc to interrupt</span> */}
    </div>
  )
}
