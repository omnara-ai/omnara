
import { RetroCard, RetroCardContent, RetroCardHeader, RetroCardTitle } from '@/components/ui/RetroCard'
import { Activity, ArrowRight, Radio } from 'lucide-react'
import { AgentType, AgentInstance } from '@/types/dashboard'
import { getTimeSince, formatAgentTypeName } from '@/utils/statusUtils'
import { useNavigate } from 'react-router-dom'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { CozyEmptyState } from '@/components/ui/CozyEmptyState'

interface RecentActivityProps {
  agentTypes: AgentType[]
}

export function RecentActivity({ agentTypes }: RecentActivityProps) {
  const navigate = useNavigate()

  // Collect all recent instances from all agent types and sort by most recent
  const allInstances = agentTypes.flatMap(type => 
    type.recent_instances.map(instance => ({
      ...instance,
      agent_type_name: formatAgentTypeName(type.name)
    }))
  ).sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime())
  .slice(0, 8) // Show last 8 activities

  const getActivityIcon = (status: string) => {
    switch (status) {
      case 'ACTIVE':
        return '🟢'
      case 'AWAITING_INPUT':
        return '🟡'
      case 'COMPLETED':
        return '✅'
      case 'FAILED':
        return '🔴'
      case 'PAUSED':
        return '⏸️'
      default:
        return '⚫'
    }
  }

  const getStatusTooltip = (status: string) => {
    switch (status) {
      case 'ACTIVE':
        return 'Status: Currently Active'
      case 'AWAITING_INPUT':
        return 'Status: Waiting for Input'
      case 'COMPLETED':
        return 'Status: Completed Successfully'
      case 'FAILED':
        return 'Status: Task Failed'
      case 'PAUSED':
        return 'Status: Paused by User'
      default:
        return 'Status: Unknown'
    }
  }

  const getActivityDescription = (instance: AgentInstance) => {
    switch (instance.status) {
      case 'ACTIVE':
        return `Started ${instance.latest_message || 'new task'}`
      case 'AWAITING_INPUT':
        return 'Awaiting human input'
      case 'COMPLETED':
        return 'Completed successfully'
      case 'FAILED':
        return 'Task failed'
      case 'PAUSED':
        return 'Paused by user'
      default:
        return 'Status updated'
    }
  }

  return (
    <TooltipProvider>
      <RetroCard className="border-cozy-amber/20 h-full terminal-effect">
        <RetroCardHeader>
          <RetroCardTitle className="flex items-center justify-between">
            <div className="flex items-center space-x-2">
              <Radio className="h-5 w-5 text-cozy-amber icon-hover" />
              <span className="text-lg font-bold tracking-wide uppercase">Recent Activity</span>
            </div>
            <button 
              onClick={() => navigate('/dashboard/instances')}
              className="text-sm text-cream hover:text-soft-gold transition-colors font-medium flex items-center gap-1"
            >
              View All Activity
              <ArrowRight className="h-4 w-4" />
            </button>
          </RetroCardTitle>
        </RetroCardHeader>
        <RetroCardContent className="space-y-2 overflow-y-auto scrollbar-thin scrollbar-thumb-cozy-amber/20">
          {allInstances.length === 0 ? (
            <CozyEmptyState
              variant="starry-sky"
              title="No recent activity"
              description="Your agents are quietly waiting for their next adventure"
              className="py-8"
            />
          ) : (
            <div className="space-y-1">
              {allInstances.map((instance, index) => (
                <div 
                  key={instance.id} 
                  className="flex items-start justify-between py-3 px-4 rounded cursor-pointer transition-all duration-200 hover:bg-warm-charcoal/30 hover:translate-x-1 group border-l-2 border-cozy-amber/30"
                  onClick={() => navigate(`/dashboard/instances/${instance.id}`)}
                >
                  <div className="flex items-start space-x-3 flex-1">
                    <div className="flex flex-col items-center">
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <span className="text-sm cursor-help">{getActivityIcon(instance.status)}</span>
                        </TooltipTrigger>
                        <TooltipContent className="bg-warm-charcoal border-cozy-amber/20">
                          <p className="text-cream">{getStatusTooltip(instance.status)}</p>
                        </TooltipContent>
                      </Tooltip>
                      {index < allInstances.length - 1 && (
                        <div className="w-px h-12 bg-cozy-amber/20 mt-1" />
                      )}
                    </div>
                    <div className="flex-1">
                      <p className="text-cream text-sm leading-tight">
                        <span className="text-retro-terminal font-mono">&gt;</span> {getActivityDescription(instance)}
                      </p>
                      <div className="flex items-center space-x-2 text-xs text-cream/70 mt-1">
                        <span>{instance.agent_type_name}</span>
                        <span className="text-cozy-amber/50">|</span>
                        <span>{getTimeSince(instance.started_at)}</span>
                      </div>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </RetroCardContent>
      </RetroCard>
    </TooltipProvider>
  )
}
