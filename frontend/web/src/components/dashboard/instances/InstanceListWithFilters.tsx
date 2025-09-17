import { useState, useEffect } from 'react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { InstanceTable } from './InstanceTable'
import { usePolling } from '@/hooks/usePolling'
import { apiClient } from '@/lib/dashboardApi'
import { AgentInstance, AgentStatus } from '@/types/dashboard'

export function InstanceListWithFilters() {
  const [instances, setInstances] = useState<AgentInstance[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState('active')

  const fetchAllInstances = async () => {
    try {
      // Try the new getAllInstances endpoint first, fallback to agent types approach
      let allInstances: AgentInstance[] = []
      
      try {
        allInstances = await apiClient.getAllInstances(undefined, 'all')
      } catch (err) {
        console.log('Fallback to agent types method for instances')
        // Fallback: Get all agent types and their instances
        const types = await apiClient.getAgentTypes()
        allInstances = types.flatMap(type => 
          type.recent_instances.map(instance => ({
            ...instance,
            agent_type_name: type.name
          }))
        )
      }

      // If we got instances from the direct endpoint, we need to get agent types for names
      if (allInstances.length > 0 && !allInstances[0].agent_type_name) {
        const types = await apiClient.getAgentTypes()
        const typeMap = new Map(types.map(type => [type.id, type.name]))
        
        allInstances = allInstances.map(instance => ({
          ...instance,
          agent_type_name: typeMap.get(instance.agent_type_id) || 'Unknown'
        }))
      }
      
      setInstances(allInstances)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch instances')
    } finally {
      setLoading(false)
    }
  }

  usePolling(fetchAllInstances, 5000)

  useEffect(() => {
    fetchAllInstances()
  }, [])

  const filteredInstances = instances.filter(instance => {
    switch (activeFilter) {
      case 'active':
        return instance.status === AgentStatus.ACTIVE || instance.status === AgentStatus.AWAITING_INPUT
      case 'completed':
        return instance.status === AgentStatus.COMPLETED
      case 'all':
      default:
        return true
    }
  })

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
      <div>
        <h2 className="text-3xl font-bold tracking-tight text-white bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent">
          All Agent Instances
        </h2>
        <p className="text-off-white/80">
          Complete overview of all agent instances across all types ({instances.length} total)
        </p>
      </div>

      <Tabs value={activeFilter} onValueChange={setActiveFilter} className="w-full">
        <TabsList className="grid w-full max-w-md grid-cols-3 bg-white/10 border border-white/20">
          <TabsTrigger value="active" className="data-[state=active]:bg-white/20">
            Active ({instances.filter(i => i.status === AgentStatus.ACTIVE || i.status === AgentStatus.AWAITING_INPUT).length})
          </TabsTrigger>
          <TabsTrigger value="completed" className="data-[state=active]:bg-white/20">
            Completed ({instances.filter(i => i.status === AgentStatus.COMPLETED).length})
          </TabsTrigger>
          <TabsTrigger value="all" className="data-[state=active]:bg-white/20">
            All ({instances.length})
          </TabsTrigger>
        </TabsList>

        <TabsContent value="active" className="mt-6">
          <InstanceTable instances={filteredInstances} showAgentType={true} onInstancesUpdate={fetchAllInstances} />
        </TabsContent>

        <TabsContent value="completed" className="mt-6">
          <InstanceTable instances={filteredInstances} showAgentType={true} onInstancesUpdate={fetchAllInstances} />
        </TabsContent>

        <TabsContent value="all" className="mt-6">
          <InstanceTable instances={filteredInstances} showAgentType={true} onInstancesUpdate={fetchAllInstances} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
