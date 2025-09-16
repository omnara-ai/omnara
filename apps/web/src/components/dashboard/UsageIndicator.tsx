import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { useFeatureAccess } from '@/hooks/useSubscription'
import { Sparkles, AlertTriangle, TrendingUp } from 'lucide-react'
import { cn } from '@/lib/utils'

export function UsageIndicator() {
  const { subscription, usage, isLoading } = useFeatureAccess()

  // Don't show for loading state, unlimited plans, or non-free plans
  if (isLoading || !subscription || !usage || subscription.agent_limit === -1 || subscription.plan_type !== 'free') {
    return null
  }

  const usagePercentage = Math.min(100, (usage.total_agents / subscription.agent_limit) * 100)
  const isApproachingLimit = usagePercentage >= 80
  const hasReachedLimit = usage.total_agents >= subscription.agent_limit

  // Color scheme based on usage
  const getProgressColor = () => {
    if (hasReachedLimit) return 'bg-red-500'
    if (isApproachingLimit) return 'bg-amber-500'
    return 'bg-white/60' // Brighter white for better contrast
  }

  const getCountColor = () => {
    if (hasReachedLimit) return 'text-red-400'
    if (isApproachingLimit) return 'text-amber-400'
    return 'text-white'
  }

  const getButtonStyles = () => {
    if (hasReachedLimit) {
      return 'bg-red-600 hover:bg-red-700 text-white border-0'
    }
    if (isApproachingLimit) {
      return 'bg-amber-500/25 hover:bg-amber-500/35 text-amber-400 border-amber-500/50'
    }
    // More vibrant electric violet for normal state
    return 'bg-[#9333EA]/25 hover:bg-[#9333EA]/35 text-white border-[#9333EA]/40'
  }

  return (
    <div className="bg-white/[0.03] rounded-lg p-3 space-y-3 border border-white/10">
      {/* Usage Stats */}
      <div className="flex items-center justify-between">
        <span className="text-xs text-text-secondary font-normal">
          Agents Used
        </span>
        <span className={cn("text-sm font-bold font-mono", getCountColor())}>
          {usage.total_agents}
          <span className="text-text-secondary font-medium">/{subscription.agent_limit}</span>
        </span>
      </div>

      {/* Progress Bar */}
      <div className="relative h-2 bg-black/30 rounded-full overflow-hidden">
        <div 
          className={cn("absolute inset-y-0 left-0 transition-all duration-500 rounded-full", getProgressColor())}
          style={{ width: `${usagePercentage}%` }}
        />
      </div>

      {/* Action Button */}
      <Link to="/pricing" className="block">
        <Button 
          size="sm" 
          variant="outline"
          className={cn(
            "w-full text-xs font-semibold h-8 transition-all duration-200",
            getButtonStyles()
          )}
        >
          {hasReachedLimit ? (
            <>
              <AlertTriangle className="w-3 h-3 mr-1.5" />
              Upgrade to Continue
            </>
          ) : isApproachingLimit ? (
            <>
              <TrendingUp className="w-3 h-3 mr-1.5" />
              Upgrade Now
            </>
          ) : (
            <>
              <Sparkles className="w-3.5 h-3.5 mr-1.5" />
              Upgrade for more
            </>
          )}
        </Button>
      </Link>
    </div>
  )
}