import { Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { AgentType, AgentStatus } from '@/types/dashboard'
import { Bot, HelpCircle } from 'lucide-react'
import { getStatusIcon, getStatusColor, getStatusLabel, getTimeSince, formatAgentTypeName } from '@/utils/statusUtils'

interface AgentCardProps {
  agentType: AgentType
}

export function AgentCard({ agentType }: AgentCardProps) {
  const waitingInstances = agentType.recent_instances.filter(
    instance => instance.status === 'AWAITING_INPUT'
  ).length

  return (
    <Card className="border border-white/20 bg-white/10 backdrop-blur-md hover:bg-white/15 transition-all duration-500 hover:shadow-xl hover:shadow-electric-blue/20 hover:scale-[1.02] transform-gpu group cursor-pointer">
      <CardHeader className="flex flex-row items-center space-y-0 pb-4">
        <div className="flex items-center space-x-4 flex-1">
          <div className="p-3 rounded-xl bg-electric-blue/20 border border-electric-accent/30 backdrop-blur-sm group-hover:bg-electric-blue/40 group-hover:shadow-lg group-hover:shadow-electric-accent/30 transition-all duration-500 group-hover:scale-110 transform-gpu">
            <Bot className="h-8 w-8 text-electric-accent group-hover:text-white transition-colors duration-300" />
          </div>
          <CardTitle className="text-2xl font-bold text-white">{formatAgentTypeName(agentType.name)}</CardTitle>
        </div>
        {waitingInstances > 0 && (
          <Badge variant="destructive" className="flex items-center space-x-2 px-3 py-1 text-sm font-semibold bg-red-500/20 border-red-400/30 text-red-200 hover:bg-red-500/30">
            <HelpCircle className="h-4 w-4" />
            <span>{waitingInstances}</span>
          </Badge>
        )}
      </CardHeader>
      
      <CardContent className="space-y-6 pt-2">
        <div className="space-y-3">
          {agentType.recent_instances.length > 0 ? (
            agentType.recent_instances.map((instance) => {
              const statusLabel = getStatusLabel(instance.status, instance.last_signal_at)
              
              return (
                <Link
                  key={instance.id}
                  to={`/dashboard/instances/${instance.id}`}
                  className="block"
                >
                  <div className="flex flex-col space-y-3 p-4 rounded-xl bg-white/5 border border-white/10 hover:bg-white/10 hover:shadow-lg hover:shadow-electric-blue/5 transition-all duration-300 cursor-pointer backdrop-blur-sm">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center space-x-3">
                        <div className="flex items-center justify-center w-8 h-8 rounded-lg bg-white/10 backdrop-blur-sm">
                          <span className="text-lg">{getStatusIcon(instance.status)}</span>
                        </div>
                        <span className="text-base font-semibold text-off-white">
                          Instance {instance.id.slice(-8)}
                        </span>
                      </div>
                      <div className="flex items-center space-x-3">
                        <Badge className={`text-sm px-3 py-1 ${getStatusColor(instance.status)}`}>
                          {statusLabel}
                        </Badge>
                        <span className="text-sm text-off-white/70 font-medium">
                          {getTimeSince(instance.started_at)}
                        </span>
                      </div>
                    </div>
                    {instance.latest_message && (
                      <div className="text-sm text-off-white/80 bg-white/5 p-2 rounded border-l-2 border-electric-accent/50 backdrop-blur-sm">
                        Current: {instance.latest_message}
                      </div>
                    )}
                  </div>
                </Link>
              )
            })
          ) : (
            <div className="text-lg text-off-white/70 text-center py-8 font-medium">
              No instances yet
            </div>
          )}
        </div>
        
        <Button asChild className="w-full h-12 text-lg font-semibold bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0">
          <Link to={`/dashboard/agent-types/${agentType.id}`}>
            View All Instances
          </Link>
        </Button>
      </CardContent>
    </Card>
  )
}
