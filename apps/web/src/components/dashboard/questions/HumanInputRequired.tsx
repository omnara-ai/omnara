
import { RetroCard, RetroCardContent } from '@/components/ui/RetroCard'
import { User } from 'lucide-react'
import { AgentInstance } from '@/types/dashboard'
interface HumanInputRequiredProps {
  instances: AgentInstance[]
}

export function HumanInputRequired({ instances }: HumanInputRequiredProps) {
  if (instances.length === 0) {
    return (
      <RetroCard className="border border-sage-green/30 bg-sage-green/10">
        <RetroCardContent className="p-6">
          <div className="flex items-center justify-between">
            <div className="flex items-center space-x-4">
              <div className="p-3 bg-sage-green/20 rounded-full">
                <User className="h-6 w-6 text-sage-green" />
              </div>
              <div>
                <h3 className="text-lg font-semibold text-cream">All Clear</h3>
                <p className="text-cream/80">No agents require human input at this time</p>
              </div>
            </div>
          </div>
        </RetroCardContent>
      </RetroCard>
    )
  }

  // Simple list of instances requiring input
  return (
    <div className="space-y-4">
      {instances.map(instance => (
        <RetroCard key={instance.id} className="border border-amber-500/30 bg-amber-500/10">
          <RetroCardContent className="p-4">
            <div className="flex items-center space-x-3">
              <div className="p-2 bg-amber-500/20 rounded-full">
                <User className="h-4 w-4 text-amber-500" />
              </div>
              <div>
                <h4 className="font-medium text-cream">{instance.agent_type}</h4>
                <p className="text-sm text-cream/80">Waiting for input</p>
              </div>
            </div>
          </RetroCardContent>
        </RetroCard>
      ))}
    </div>
  )
}
