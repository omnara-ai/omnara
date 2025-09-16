
import { useState, useEffect, useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { Breadcrumb } from './../Breadcrumb'
import { InstanceTable } from './InstanceTable'
import { usePolling } from '@/hooks/usePolling'
import { apiClient } from '@/lib/dashboardApi'
import { AgentInstance } from '@/types/dashboard'

export function InstanceList() {
  const { agentId } = useParams<{ agentId?: string }>()
  const [instances, setInstances] = useState<AgentInstance[]>([])
  const [agentName, setAgentName] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchInstances = useCallback(async () => {
    if (!agentId) return
    
    try {
      const [userAgentInstances, userAgents] = await Promise.all([
        apiClient.getUserAgentInstances(agentId),
        apiClient.getUserAgents()
      ])
      
      setInstances(userAgentInstances)
      
      const userAgent = userAgents.find(agent => agent.id === agentId)
      setAgentName(userAgent?.name || 'Unknown Agent')
      
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch instances')
    } finally {
      setLoading(false)
    }
  }, [agentId])

  usePolling(fetchInstances, 5000)

  useEffect(() => {
    fetchInstances()
  }, [fetchInstances])

  const displayName = agentName || 'Agent'

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <div className="text-off-white/80 animate-fade-in">Loading instances...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <div className="text-red-400 animate-fade-in">Error: {error}</div>
      </div>
    )
  }

  return (
    <div className="space-y-6 animate-fade-in">
      <Breadcrumb 
        items={[
          { label: displayName }
        ]} 
      />

      <div>
        <h2 className="text-3xl font-bold tracking-tight text-white bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent">
          {displayName} Instances
        </h2>
        <p className="text-off-white/80">
          Monitor and manage all instances for this agent
        </p>
      </div>

      <InstanceTable instances={instances} showAgentType={false} onInstancesUpdate={fetchInstances} />
    </div>
  )
}
