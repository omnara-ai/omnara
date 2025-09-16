
import { Link, useNavigate } from 'react-router-dom'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AgentInstance, AgentStatus } from '@/types/dashboard'
import { getStatusIcon, getStatusColor, getStatusLabel, getTimeSince, getDuration, formatAgentTypeName } from '@/utils/statusUtils'
import { ArrowUpDown, Bot, CheckCircle } from 'lucide-react'
import { useState } from 'react'
import { CopyButton } from './../CopyButton'
import { apiClient } from '@/lib/dashboardApi'
import { useMobile } from '@/hooks/use-mobile'
import { MobileInstanceCard } from './MobileInstanceCard'

interface InstanceTableProps {
  instances: AgentInstance[]
  showAgentType?: boolean
  onInstancesUpdate?: () => void
}

type SortField = 'started_at' | 'status' | 'duration' | 'steps'
type SortDirection = 'asc' | 'desc'

export function InstanceTable({ instances, showAgentType = true, onInstancesUpdate }: InstanceTableProps) {
  const navigate = useNavigate()
  const isMobile = useMobile()
  const [sortField, setSortField] = useState<SortField>('started_at')
  const [sortDirection, setSortDirection] = useState<SortDirection>('desc')
  const [selectedInstances, setSelectedInstances] = useState<Set<string>>(new Set())
  const [bulkCompleting, setBulkCompleting] = useState(false)

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDirection(sortDirection === 'asc' ? 'desc' : 'asc')
    } else {
      setSortField(field)
      setSortDirection('desc')
    }
  }

  const handleRowClick = (instanceId: string, event: React.MouseEvent) => {
    // Don't navigate if clicking on checkbox
    if ((event.target as HTMLElement).closest('[data-checkbox]')) {
      return
    }
    navigate(`/dashboard/instances/${instanceId}`)
  }

  const handleSelectInstance = (instanceId: string, checked: boolean) => {
    const newSelected = new Set(selectedInstances)
    if (checked) {
      newSelected.add(instanceId)
    } else {
      newSelected.delete(instanceId)
    }
    setSelectedInstances(newSelected)
  }

  const handleSelectAll = (checked: boolean) => {
    if (checked) {
      setSelectedInstances(new Set(instances.map(i => i.id)))
    } else {
      setSelectedInstances(new Set())
    }
  }

  const handleBulkMarkCompleted = async () => {
    if (selectedInstances.size === 0 || bulkCompleting) return
    
    setBulkCompleting(true)
    try {
      await Promise.all(
        Array.from(selectedInstances).map(instanceId =>
          apiClient.updateInstanceStatus(instanceId, AgentStatus.COMPLETED)
        )
      )
      setSelectedInstances(new Set())
      onInstancesUpdate?.()
    } catch (err) {
      console.error('Failed to mark instances as completed:', err)
    } finally {
      setBulkCompleting(false)
    }
  }

  const getChatLength = (instance: AgentInstance) => {
    return instance.chat_length || 0
  }

  const sortedInstances = [...instances].sort((a, b) => {
    let aValue: any, bValue: any

    switch (sortField) {
      case 'started_at':
        aValue = new Date(a.started_at).getTime()
        bValue = new Date(b.started_at).getTime()
        break
      case 'status':
        aValue = a.status
        bValue = b.status
        break
      case 'duration':
        aValue = a.ended_at 
          ? new Date(a.ended_at).getTime() - new Date(a.started_at).getTime()
          : Date.now() - new Date(a.started_at).getTime()
        bValue = b.ended_at 
          ? new Date(b.ended_at).getTime() - new Date(b.started_at).getTime()
          : Date.now() - new Date(b.started_at).getTime()
        break
      case 'steps':
        aValue = getChatLength(a)
        bValue = getChatLength(b)
        break
      default:
        return 0
    }

    if (sortDirection === 'asc') {
      return aValue > bValue ? 1 : -1
    } else {
      return aValue < bValue ? 1 : -1
    }
  })

  if (instances.length === 0) {
    return (
      <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
        <CardContent className="flex items-center justify-center min-h-[200px]">
          <div className="text-center space-y-2">
            <div className="text-off-white/70 text-lg">No instances found</div>
            <div className="text-sm text-off-white/60">
              Instances will appear here once agents start running
            </div>
          </div>
        </CardContent>
      </Card>
    )
  }

  // Mobile view with cards
  if (isMobile) {
    return (
      <div className="space-y-4">
        {selectedInstances.size > 0 && (
          <Card className="border border-white/20 bg-white/10 backdrop-blur-md p-4">
            <Button 
              onClick={handleBulkMarkCompleted}
              disabled={bulkCompleting}
              className="w-full bg-gray-500/20 border border-gray-400/30 text-gray-200 hover:bg-gray-500/30"
            >
              <CheckCircle className="h-4 w-4 mr-2" />
              {bulkCompleting ? 'Marking...' : `Mark ${selectedInstances.size} as Completed`}
            </Button>
          </Card>
        )}
        <div className="grid gap-3">
          {sortedInstances.map((instance) => (
            <MobileInstanceCard 
              key={instance.id} 
              instance={instance}
              onClick={() => navigate(`/dashboard/instances/${instance.id}`)}
            />
          ))}
        </div>
      </div>
    )
  }

  // Desktop table view
  return (
    <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-white">
            {showAgentType ? 'All Instances' : 'Agent Instances'}
          </CardTitle>
          {selectedInstances.size > 0 && (
            <Button 
              onClick={handleBulkMarkCompleted}
              disabled={bulkCompleting}
              className="bg-gray-500/20 border border-gray-400/30 text-gray-200 hover:bg-gray-500/30"
            >
              <CheckCircle className="h-4 w-4 mr-2" />
              {bulkCompleting ? 'Marking...' : `Mark ${selectedInstances.size} as Completed`}
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>
        <div className="overflow-x-auto">
          <table className="w-full table-fixed">
            <thead>
              <tr className="border-b border-white/20">
                <th className="text-left text-off-white py-3 px-4 w-12">
                  <Checkbox
                    checked={selectedInstances.size === instances.length && instances.length > 0}
                    onCheckedChange={handleSelectAll}
                    data-checkbox
                  />
                </th>
                <th 
                  className="text-left text-off-white cursor-pointer hover:text-white transition-colors py-3 px-4 w-32"
                  onClick={() => handleSort('status')}
                >
                  <div className="flex items-center space-x-1">
                    <span>Status</span>
                    <ArrowUpDown className="h-4 w-4" />
                  </div>
                </th>
                {showAgentType && (
                  <th className="text-left text-off-white py-3 px-4 w-40">Agent Type</th>
                )}
                <th className="text-left text-off-white py-3 px-4">Instance</th>
                <th 
                  className="text-left text-off-white cursor-pointer hover:text-white transition-colors py-3 px-4 w-40"
                  onClick={() => handleSort('duration')}
                >
                  <div className="flex items-center space-x-1">
                    <span>Duration</span>
                    <ArrowUpDown className="h-4 w-4" />
                  </div>
                </th>
                <th 
                  className="text-left text-off-white cursor-pointer hover:text-white transition-colors py-3 px-4 w-32"
                  onClick={() => handleSort('steps')}
                >
                  <div className="flex items-center space-x-1">
                    <span>Messages</span>
                    <ArrowUpDown className="h-4 w-4" />
                  </div>
                </th>
                <th 
                  className="text-left text-off-white cursor-pointer hover:text-white transition-colors py-3 px-4 w-40"
                  onClick={() => handleSort('started_at')}
                >
                  <div className="flex items-center space-x-1">
                    <span>Started</span>
                    <ArrowUpDown className="h-4 w-4" />
                  </div>
                </th>
              </tr>
            </thead>
            <tbody>
              {sortedInstances.map((instance) => {
                const statusLabel = getStatusLabel(instance.status, instance.last_signal_at)
                const duration = instance.ended_at 
                  ? getDuration(instance.started_at, instance.ended_at)
                  : getDuration(instance.started_at)
                const chatLength = getChatLength(instance)

                return (
                  <tr 
                    key={instance.id} 
                    className="border-b border-white/10 hover:bg-white/5 transition-colors cursor-pointer"
                    onClick={(e) => handleRowClick(instance.id, e)}
                  >
                    <td className="py-4 px-4">
                      <Checkbox
                        checked={selectedInstances.has(instance.id)}
                        onCheckedChange={(checked) => handleSelectInstance(instance.id, checked as boolean)}
                        data-checkbox
                      />
                    </td>
                    <td className="py-4 px-4">
                      <Badge className={`flex items-center w-fit text-sm ${getStatusIcon(instance.status) ? 'space-x-1' : ''} ${getStatusColor(instance.status)}`}>
                        {getStatusIcon(instance.status) && <span>{getStatusIcon(instance.status)}</span>}
                        <span>{statusLabel}</span>
                      </Badge>
                    </td>
                    {showAgentType && (
                      <td className="py-4 px-4">
                        <div className="flex items-center space-x-2">
                          <Bot className="h-5 w-5 text-electric-accent" />
                          <span className="text-off-white font-medium text-base">
                            {formatAgentTypeName(instance.agent_type_name || 'Unknown')}
                          </span>
                        </div>
                      </td>
                    )}
                    <td className="py-4 px-4">
                      <div className="space-y-1">
                        {instance.latest_message && (
                          <div className="text-off-white text-base leading-relaxed line-clamp-2 font-normal">
                            {instance.latest_message}
                          </div>
                        )}
                        <div className="flex items-center space-x-2 text-sm leading-tight">
                          <span className="text-gray-400 font-mono">ID: {instance.id.slice(-8)}</span>
                          <CopyButton text={instance.id} label="" />
                        </div>
                      </div>
                    </td>
                    <td className="py-4 px-4 text-off-white text-base">
                      {duration}
                    </td>
                    <td className="py-4 px-4 text-off-white text-base">
                      {chatLength}
                    </td>
                    <td className="py-4 px-4 text-off-white text-base">
                      {getTimeSince(instance.started_at)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
  )
}
