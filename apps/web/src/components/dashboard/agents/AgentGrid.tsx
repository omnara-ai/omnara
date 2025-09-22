import { useState, useEffect } from 'react'
import { AgentCard } from './AgentCard'
import { usePolling } from '@/hooks/usePolling'
import { apiClient } from '@/lib/dashboardApi'
import { AgentType } from '@/types/dashboard'

export function AgentGrid() {
  const [agentTypes, setAgentTypes] = useState<AgentType[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchAgentTypes = async () => {
    try {
      const types = await apiClient.getAgentTypes()
      setAgentTypes(types)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch agent types')
    } finally {
      setLoading(false)
    }
  }

  usePolling(fetchAgentTypes, 5000)

  useEffect(() => {
    fetchAgentTypes()
  }, [])

  if (loading) {
    return (
      <div className="space-y-8 animate-fade-in">
        <div className="text-center space-y-3">
          <h2 className="text-4xl font-bold tracking-tight text-white bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent">
            Loading Your AI Agents
          </h2>
          <p className="text-xl text-off-white/80 max-w-2xl mx-auto leading-relaxed">
            Fetching your agent fleet...
          </p>
        </div>
        
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-8">
          {[...Array(6)].map((_, index) => (
            <div
              key={index}
              className="animate-pulse border border-white/20 bg-white/10 backdrop-blur-md rounded-lg p-6 animate-fade-in"
              style={{ animationDelay: `${index * 0.1}s` }}
            >
              <div className="flex items-center space-x-4 mb-6">
                <div className="w-14 h-14 bg-white/20 rounded-xl animate-pulse"></div>
                <div className="space-y-2 flex-1">
                  <div className="h-6 bg-white/20 rounded w-3/4 animate-pulse"></div>
                  <div className="h-4 bg-white/10 rounded w-1/2 animate-pulse"></div>
                </div>
              </div>
              <div className="space-y-4">
                <div className="h-24 bg-white/10 rounded animate-pulse"></div>
                <div className="h-12 bg-white/20 rounded animate-pulse"></div>
              </div>
            </div>
          ))}
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex items-center justify-center min-h-[500px]">
        <div className="text-xl text-red-400 font-medium animate-fade-in">Error: {error}</div>
      </div>
    )
  }

  return (
    <div className="space-y-8 animate-fade-in">
      <div className="text-center space-y-3">
        <h2 className="text-4xl font-bold tracking-tight text-white bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent">
          Your AI Agents
        </h2>
        <p className="text-xl text-off-white/80 max-w-2xl mx-auto leading-relaxed">
          Monitor active sessions, respond to questions, and track your AI agents' progress
        </p>
      </div>
      
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-8">
        {agentTypes.map((agentType, index) => (
          <div 
            key={agentType.id} 
            className="animate-fade-in"
            style={{ animationDelay: `${index * 0.1}s` }}
          >
            <AgentCard agentType={agentType} />
          </div>
        ))}
      </div>
      
      {agentTypes.length === 0 && (
        <div className="flex items-center justify-center min-h-[500px]">
          <div className="text-center space-y-4 animate-fade-in">
            <div className="text-off-white/70 text-2xl font-medium">No agents found</div>
            <div className="text-lg text-off-white/60">
              Connect an agent to get started
            </div>
          </div>
        </div>
      )}
    </div>
  )
} 