
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Activity, CheckCircle, DollarSign } from 'lucide-react'

interface KPICardsProps {
  activeAgents: number
  tasksCompleted: number
  isLoading?: boolean
}

export function KPICards({ activeAgents, tasksCompleted, isLoading }: KPICardsProps) {
  if (isLoading) {
    return (
      <div className="space-y-4">
        {[1, 2].map((i) => (
          <div key={i} className="h-24 bg-white/10 rounded-lg animate-pulse" />
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <Card className="bg-blue-500/10 border-blue-400/30 backdrop-blur-sm">
        <CardContent className="p-6">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-2xl font-bold text-white">{activeAgents}</p>
              <p className="text-sm text-blue-200">Active Agents</p>
            </div>
            <Activity className="h-8 w-8 text-blue-400" />
          </div>
        </CardContent>
      </Card>

      <Card className="bg-green-500/10 border-green-400/30 backdrop-blur-sm">
        <CardContent className="p-6">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-2xl font-bold text-white">{tasksCompleted}</p>
              <p className="text-sm text-green-200">Tasks Completed</p>
            </div>
            <CheckCircle className="h-8 w-8 text-green-400" />
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
