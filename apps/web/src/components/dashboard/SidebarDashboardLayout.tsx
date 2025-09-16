import { useState, useEffect } from 'react'
import { Outlet, Link, useNavigate, useLocation } from 'react-router-dom'
import { useAuth } from '@/lib/auth/AuthContext'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { 
  User, 
  LogOut, 
  Settings, 
  CreditCard, 
  ChevronRight,
  ChevronDown,
  Plus,
  Search,
  Menu,
  X,
  Home,
  Bot,
  MessageSquare,
  Clock,
  CheckCircle,
  AlertCircle,
  Key,
  MoreHorizontal,
  Edit2,
  Trash2,
  Copy,
  Link2,
  Archive
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { GhibliBackground } from './GhibliBackground'
import ClaudeIcon from '@/components/claude-color.svg'
import OpenAIIcon from '@/components/openai.svg'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'
import { AgentInstance, AgentStatus } from '@/types/dashboard'
import { formatAgentTypeName, getTimeSince } from '@/utils/statusUtils'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { toast } from 'sonner'
import { WebhookConfigModal } from './WebhookConfigModal'
import { LaunchAgentModal } from './LaunchAgentModal'
import { CommandPalette } from './CommandPalette'
import { UsageIndicator } from './UsageIndicator'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

interface SidebarAgentGroup {
  id: string
  name: string
  originalName: string
  instances: AgentInstance[]
  isExpanded: boolean
  hasWebhook: boolean
  userAgent?: any
}

export function SidebarDashboardLayout() {
  const { user, signOut } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const queryClient = useQueryClient()
  const [isMobileSidebarOpen, setIsMobileSidebarOpen] = useState(false)
  const [agentGroups, setAgentGroups] = useState<SidebarAgentGroup[]>([])
  const [searchQuery, setSearchQuery] = useState('')
  const [isPromptOpen, setIsPromptOpen] = useState(false)
  const [selectedAgent, setSelectedAgent] = useState<any | null>(null)
  
  // Rename dialog state
  const [renameDialogOpen, setRenameDialogOpen] = useState(false)
  const [instanceToRename, setInstanceToRename] = useState<AgentInstance | null>(null)
  const [newInstanceName, setNewInstanceName] = useState('')
  
  // Delete dialog state
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [instanceToDelete, setInstanceToDelete] = useState<AgentInstance | null>(null)
  
  // Agent delete dialog state
  const [deleteAgentDialogOpen, setDeleteAgentDialogOpen] = useState(false)
  const [agentToDelete, setAgentToDelete] = useState<SidebarAgentGroup | null>(null)
  
  // Agent selection dialog state
  const [agentSelectionOpen, setAgentSelectionOpen] = useState(false)
  
  // Create agent dialog state
  const [createAgentDialogOpen, setCreateAgentDialogOpen] = useState(false)
  const [newAgentName, setNewAgentName] = useState('')
  
  // Command palette state
  const [isCommandPaletteOpen, setIsCommandPaletteOpen] = useState(false)
  
  // Archived sessions modal state
  const [archivedSessionsOpen, setArchivedSessionsOpen] = useState(false)
  
  // Webhook edit dialog state (for New Chat flow)
  const [webhookDialogOpen, setWebhookDialogOpen] = useState(false)
  const [selectedAgentForWebhook, setSelectedAgentForWebhook] = useState<{
    id: string, 
    name: string, 
    webhook_type?: string,
    webhook_config?: Record<string, any>,
    is_active?: boolean
  } | null>(null)

  // Fetch agent data
  const { data: agentTypes, isLoading } = useQuery({
    queryKey: ['agent-types'],
    queryFn: () => apiClient.getAgentTypes(),
    refetchInterval: 5000,
  })

  // Fetch user agents for webhook info
  const { data: userAgents } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => apiClient.getUserAgents(),
    refetchInterval: 5000,
  })

  // Process agent data into sidebar groups
  useEffect(() => {
    if (agentTypes && userAgents) {
      setAgentGroups(prevGroups => {
        // Create maps of current expansion states
        const expansionStates = new Map(
          prevGroups.map(group => [group.id, group.isExpanded])
        )

        
        // Build new groups preserving expansion states
        const groups = agentTypes.map(agentType => {
          const userAgent = userAgents.find(ua => ua.name === agentType.name)
          return {
            id: agentType.id,
            name: formatAgentTypeName(agentType.name),
            originalName: agentType.name, // Keep original name for matching
            instances: agentType.recent_instances,
            // Preserve existing expansion states, default to true for active instances
            isExpanded: expansionStates.get(agentType.id) ?? true,
            hasWebhook: !!userAgent?.webhook_type,
            userAgent: userAgent // Store the full userAgent object
          }
        })
        
        return groups
      })
    }
  }, [agentTypes, userAgents])

  // Choose provider logo based on agent type name
  const getAgentLogo = (agentTypeName: string) => {
    const name = (agentTypeName || '').toLowerCase()
    if (name.includes('openai') || name.includes('codex')) return OpenAIIcon
    return ClaudeIcon
  }

  // Toggle agent group expansion
  const toggleAgentGroup = (groupId: string) => {
    setAgentGroups(prev => 
      prev.map(group => 
        group.id === groupId 
          ? { ...group, isExpanded: !group.isExpanded }
          : group
      )
    )
  }



  // Filter instances based on search
  const filteredGroups = agentGroups.map(group => ({
    ...group,
    instances: group.instances.filter(instance => 
      instance.name?.toLowerCase().includes(searchQuery.toLowerCase()) ||
      instance.latest_message?.toLowerCase().includes(searchQuery.toLowerCase())
    )
  })).filter(group => 
    searchQuery === '' || 
    group.instances.length > 0 || 
    group.name.toLowerCase().includes(searchQuery.toLowerCase())
  )

  // Count instances awaiting input
  const awaitingInputCount = agentGroups.reduce((count, group) => 
    count + group.instances.filter(i => i.status === AgentStatus.AWAITING_INPUT).length,
    0
  )

  const handleSignOut = async () => {
    await signOut()
  }

  // Create agent mutation
  const createAgentMutation = useMutation({
    mutationFn: (name: string) => 
      apiClient.createUserAgent({
        name,
        is_active: true
      }),
    onSuccess: (data) => {
      toast.success('Agent created successfully')
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      setCreateAgentDialogOpen(false)
      setNewAgentName('')
      
      // After creating agent, open webhook setup dialog
      setSelectedAgentForWebhook(data)
      setWebhookDialogOpen(true)
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to create agent')
    }
  })

  // Create instance mutation
  const createInstanceMutation = useMutation({
    mutationFn: ({ agentId, prompt, name, worktreeName }: { agentId: string, prompt: string, name?: string, worktreeName?: string }) => 
      apiClient.createAgentInstance(agentId, prompt, name, worktreeName),
    onSuccess: (data) => {
      if (data.success) {
        toast.success('Agent instance created successfully')
        queryClient.invalidateQueries({ queryKey: ['user-agents'] })
        queryClient.invalidateQueries({ queryKey: ['agent-types'] })
        setIsPromptOpen(false)
        setSelectedAgent(null)
        
        if (data.agent_instance_id) {
          navigate(`/dashboard/instances/${data.agent_instance_id}`)
        }
      } else {
        toast.error(data.error || 'Failed to create agent instance')
      }
    },
    onError: (error: { toastShown?: boolean, message?: string }) => {
      if (!error.toastShown) {
        toast.error(error.message || 'Failed to create agent instance')
      }
    }
  })

  const handleLaunchAgent = (agent: any) => {
    setSelectedAgent(agent)
    setIsPromptOpen(true)
  }
  
  const handleAgentSelect = (agent: any) => {
    // Check if agent has webhook configuration by checking webhook_type
    if (agent.webhook_type) {
      // Agent has webhook type configured, proceed to launch
      handleLaunchAgent(agent)
      setAgentSelectionOpen(false)
    } else {
      // Agent needs webhook configuration
      setSelectedAgentForWebhook(agent)
      setAgentSelectionOpen(false)
      setWebhookDialogOpen(true)
    }
  }
  
  const handleUpdateWebhook = (webhookType: string, webhookConfig: Record<string, any>) => {
    if (selectedAgentForWebhook) {
      updateWebhookMutation.mutate({
        agentId: selectedAgentForWebhook.id,
        webhookType,
        webhookConfig,
        name: selectedAgentForWebhook.name,
        isActive: selectedAgentForWebhook.is_active ?? true
      })
    }
  }

  const handleSubmitPrompt = (agentId: string, runtimeData: { prompt: string; name?: string; worktreeName?: string }) => {
    createInstanceMutation.mutate({
      agentId,
      ...runtimeData
    })
  }

  // Delete instance mutation
  const deleteInstanceMutation = useMutation({
    mutationFn: (instanceId: string) => apiClient.deleteAgentInstance(instanceId),
    onSuccess: () => {
      toast.success('Chat deleted successfully')
      queryClient.invalidateQueries({ queryKey: ['agent-types'] })
      setDeleteDialogOpen(false)
      setInstanceToDelete(null)
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to delete chat')
    }
  })
  
  // Webhook update mutation
  const updateWebhookMutation = useMutation({
    mutationFn: async ({ agentId, webhookType, webhookConfig, name, isActive }: { 
      agentId: string, 
      webhookType: string,
      webhookConfig: Record<string, any>,
      name: string,
      isActive: boolean 
    }) => {
      return apiClient.updateUserAgent(agentId, { 
        webhook_type: webhookType,
        webhook_config: webhookConfig,
        name: name,
        is_active: isActive
      })
    },
    onSuccess: () => {
      toast.success('Webhook updated successfully')
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      setWebhookDialogOpen(false)
      
      // After webhook is set up, proceed to launch the agent with updated data
      if (selectedAgentForWebhook) {
        // Get the updated agent data with webhook type  
        handleLaunchAgent(selectedAgentForWebhook)
      }
      
      setSelectedAgentForWebhook(null)
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to update webhook')
    }
  })

  // Rename instance mutation
  const renameInstanceMutation = useMutation({
    mutationFn: ({ instanceId, name }: { instanceId: string, name: string }) => 
      apiClient.updateAgentInstance(instanceId, { name }),
    onSuccess: () => {
      toast.success('Chat renamed successfully')
      queryClient.invalidateQueries({ queryKey: ['agent-types'] })
      setRenameDialogOpen(false)
      setInstanceToRename(null)
      setNewInstanceName('')
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to rename chat')
    }
  })

  // Delete agent mutation
  const deleteAgentMutation = useMutation({
    mutationFn: (agentId: string) => apiClient.deleteUserAgent(agentId),
    onSuccess: () => {
      toast.success('Agent deleted successfully')
      queryClient.invalidateQueries({ queryKey: ['agent-types'] })
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      setDeleteAgentDialogOpen(false)
      setAgentToDelete(null)
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to delete agent')
    }
  })

  const handleRenameClick = (instance: AgentInstance) => {
    setInstanceToRename(instance)
    setNewInstanceName(instance.name || '')
    setRenameDialogOpen(true)
  }

  const handleDeleteClick = (instance: AgentInstance) => {
    setInstanceToDelete(instance)
    setDeleteDialogOpen(true)
  }

  const handleRenameSubmit = () => {
    if (instanceToRename && newInstanceName.trim()) {
      renameInstanceMutation.mutate({
        instanceId: instanceToRename.id,
        name: newInstanceName.trim()
      })
    }
  }

  const handleDeleteConfirm = () => {
    if (instanceToDelete) {
      deleteInstanceMutation.mutate(instanceToDelete.id)
    }
  }

  const handleDeleteAgent = (agent: SidebarAgentGroup) => {
    setAgentToDelete(agent)
    setDeleteAgentDialogOpen(true)
  }

  const handleDeleteAgentConfirm = () => {
    if (agentToDelete?.userAgent?.id) {
      deleteAgentMutation.mutate(agentToDelete.userAgent.id)
    }
  }

  // Check if we're on a specific instance page
  const instanceMatch = location.pathname.match(new RegExp('/instances/([^/]+)'))
  const instanceId = instanceMatch?.[1]

  // Keyboard shortcut for command palette
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setIsCommandPaletteOpen(true)
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [])

  const getInstanceTitle = (instance: AgentInstance) => {
    // If instance has a custom name, use it
    if (instance.name) {
      return instance.name
    }
    // Otherwise, show the latest message
    return instance.latest_message || 'No messages yet'
  }

  // Instance status dot helper (commented out for now; keep for easy restore)
  const getInstanceStatusDot = (instance: AgentInstance) => {
    // Simple, status-only indicators (no heartbeat/offline logic)
    // switch (instance.status) {
    //   case AgentStatus.ACTIVE:
    //     return <div className="w-2 h-2 bg-functional-positive rounded-full" />
    //   case AgentStatus.AWAITING_INPUT:
    //     return <div className="w-2 h-2 bg-yellow-400 rounded-full" />
    //   case AgentStatus.COMPLETED:
    //     return <CheckCircle className="w-3 h-3 text-text-secondary stroke-1" />
    //   default:
    //     return <Clock className="w-3 h-3 text-text-secondary stroke-1" />
    // }
    return <div className="w-2 h-2 bg-functional-positive rounded-full" />
  }

  // Archived sessions: revert to original look (icon-based)
  const getArchivedListIcon = (status: AgentStatus) => {
    switch (status) {
      case AgentStatus.COMPLETED:
        return <CheckCircle className="w-3 h-3 text-text-secondary stroke-1" />
      default:
        return <Clock className="w-3 h-3 text-text-secondary stroke-1" />
    }
  }

  const getStatusIndicator = (instance: AgentInstance) => {
    if (instance.status === AgentStatus.AWAITING_INPUT) {
      return "Unread"
    }
    if (instance.status === AgentStatus.ACTIVE) {
      return "Working..."
    }
    return null
  }

  const getLastActiveTime = (instance: AgentInstance) => {
    // Use the most recent timestamp available
    const timestamps = [
      instance.latest_message_at,
      instance.last_signal_at,
      instance.started_at
    ].filter(Boolean)
    
    if (timestamps.length === 0) return null
    
    // Get the most recent timestamp
    const mostRecent = timestamps.reduce((latest, current) => {
      return new Date(current!) > new Date(latest!) ? current : latest
    })
    
    return getTimeSince(mostRecent!)
  }

  const sidebar = (
    <aside className={cn(
      "h-full w-72 bg-sidebar-background border-r border-border-divider",
      "flex flex-col overflow-hidden"
    )}>
      {/* Sidebar Header - New Layout */}
      <div className="px-4 py-4">
        {/* Omnara Logo (Home Button) */}
        <Link to="/dashboard" className="block mb-4">
          <h1 className="text-text-primary text-lg font-medium hover:text-text-primary/80 transition-colors">Omnara</h1>
        </Link>
        
        {/* New Session Button */}
        <Button
          onClick={() => setAgentSelectionOpen(true)}
          className="w-full bg-transparent hover:bg-surface-panel text-text-secondary hover:text-text-primary transition-all duration-200 mb-4 rounded-lg border border-border-divider font-normal text-sm py-2.5"
        >
          <Plus className="w-4 h-4 mr-2" />
          New Session
        </Button>

        {/* Search Input */}
        <div className="relative mb-2">
          <Search className="absolute left-3 top-1/2 transform -translate-y-1/2 w-4 h-4 text-text-secondary" />
          <input
            type="text"
            placeholder="Search chats..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full bg-transparent border border-border-divider rounded-lg pl-10 pr-4 py-2.5 text-sm text-text-primary placeholder:text-text-secondary focus:outline-none focus:border-text-secondary transition-colors"
          />
        </div>
      </div>

      {/* Divider */}
      <div className="border-t border-border-divider py-2"></div>

      {/* Agent Groups - Conductor UI Style */}
      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="text-text-secondary text-sm p-4 text-center">Loading agents...</div>
        ) : filteredGroups.length === 0 ? (
          <div className="text-text-secondary text-sm p-4 text-center">
            {searchQuery ? 'No chats found' : 'No agents configured yet'}
          </div>
        ) : (
          filteredGroups.map(group => (
            <div key={group.id} className="mb-6">
              {/* Agent Type Header - Conductor Style */}
              <div className="mb-3 px-4 group/agent-header">
                <div className="flex items-center justify-between">
                  <h3 className="text-text-secondary text-sm font-medium">{group.name}</h3>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleDeleteAgent(group)}
                    className="opacity-0 group-hover/agent-header:opacity-100 transition-opacity h-6 w-6 p-0 text-text-secondary hover:text-functional-negative hover:bg-functional-negative/10"
                  >
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </div>
              </div>
              
              {/* All Instance List - Always show, simplified */}
              {(() => {
                const activeInstances = group.instances
                  .filter(i => 
                    i.status !== AgentStatus.COMPLETED && 
                    i.status !== AgentStatus.FAILED && 
                    i.status !== AgentStatus.KILLED
                  )
                  // Order by creation timestamp (newest first) within agent type
                  .sort((a, b) => {
                    const aTime = new Date(a.created_at ?? a.started_at).getTime()
                    const bTime = new Date(b.created_at ?? b.started_at).getTime()
                    return bTime - aTime
                  })
                // No need to filter completed instances anymore since they won't be shown
                
                return (
                  <>
                    {activeInstances.length > 0 && (
                      <div className="space-y-1 px-3">
                          {activeInstances.map(instance => (
                            <div
                              key={instance.id}
                              className={cn(
                                "group flex items-center justify-between pl-1 pr-2 py-2 rounded-lg hover:bg-interactive-hover transition-colors",
                                instanceId === instance.id ? "bg-interactive-hover" : ""
                              )}
                            >
                              <Link
                                to={`/dashboard/instances/${instance.id}`}
                                className="flex items-center space-x-2 flex-1 min-w-0"
                              >
                                {/* Provider logo based on agent type name */}
                                <div className="flex items-center justify-center w-4 h-4">
                                  <img src={getAgentLogo(group.originalName)} alt="Agent Provider" className="w-3 h-3" />
                                </div>
                                <div className="flex-1 min-w-0">
                                  <h4 className={cn(
                                    "text-sm font-medium truncate",
                                    instanceId === instance.id ? "text-text-primary" : "text-text-primary"
                                  )}>
                                    {getInstanceTitle(instance)}
                                  </h4>
                                  <div className="flex items-center space-x-1 mt-0.5">
                                    {/* Status indicators (Unread/Working) commented out for simplicity */}
                                    {/* {getStatusIndicator(instance) && (
                                      <>
                                        <span className={cn(
                                          "text-xs",
                                          instance.status === AgentStatus.AWAITING_INPUT ? "text-yellow-400" : "text-text-secondary"
                                        )}>
                                          {getStatusIndicator(instance)}
                                        </span>
                                        {getLastActiveTime(instance) && (
                                          <>
                                            <span className="text-xs text-text-secondary">•</span>
                                            <span className="text-xs text-text-secondary">
                                              {getLastActiveTime(instance)}
                                            </span>
                                          </>
                                        )}
                                      </>
                                    )} */}
                                    {getLastActiveTime(instance) && (
                                      <span className="text-xs text-text-secondary">
                                        {getLastActiveTime(instance)}
                                      </span>
                                    )}
                                  </div>
                                </div>
                              </Link>
                              
                              <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="opacity-0 group-hover:opacity-100 transition-opacity h-6 w-6 p-0 text-text-secondary hover:text-text-primary hover:bg-interactive-hover"
                                  >
                                    <MoreHorizontal className="h-4 w-4" />
                                  </Button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align="end" className="bg-surface-panel border-border-divider">
                                  <DropdownMenuItem 
                                    onClick={() => handleRenameClick(instance)}
                                    className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover"
                                  >
                                    <Edit2 className="w-4 h-4 mr-2" />
                                    Rename
                                  </DropdownMenuItem>
                                  {/* <DropdownMenuItem 
                                    onClick={() => navigator.clipboard.writeText(instance.id)}
                                    className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover"
                                  >
                                    <Copy className="w-4 h-4 mr-2" />
                                    Copy ID
                                  </DropdownMenuItem> */}
                                  <DropdownMenuSeparator className="bg-border-divider" />
                                  <DropdownMenuItem 
                                    onClick={() => handleDeleteClick(instance)}
                                    className="text-functional-negative hover:bg-interactive-hover focus:bg-interactive-hover"
                                  >
                                    <Trash2 className="w-4 h-4 mr-2" />
                                    Delete
                                  </DropdownMenuItem>
                                </DropdownMenuContent>
                              </DropdownMenu>
                            </div>
                          ))}
                        </div>
                      )}
                    </>
                  )
                })()}
            </div>
          ))
        )}
      </div>

      {/* Sidebar Footer */}
      <div className="px-4 pb-4">
        
        {/* Usage Indicator */}
        <div className="pb-4">
          <UsageIndicator />
        </div>
        
        {/* User Menu */}
        <div className="border-t border-border-divider pt-4">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button 
                variant="ghost" 
                size="sm"
                className="w-full justify-between text-text-secondary hover:text-text-primary hover:bg-surface-panel transition-all duration-200"
              >
                <div className="flex items-center">
                  <User className="w-4 h-4 mr-2" />
                  <span className="truncate">{user?.email}</span>
                </div>
                <ChevronRight className="w-4 h-4" />
              </Button>
            </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="bg-surface-panel border-border-divider">
            <DropdownMenuItem asChild className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover cursor-pointer">
              <Link to="/dashboard/settings">
                <Settings className="w-4 h-4 mr-2" />
                Settings
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover cursor-pointer">
              <Link to="/dashboard/api-keys">
                <Key className="w-4 h-4 mr-2" />
                API Keys
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover cursor-pointer">
              <Link to="/dashboard/billing">
                <CreditCard className="w-4 h-4 mr-2" />
                Billing
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem 
              onClick={() => setArchivedSessionsOpen(true)} 
              className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover cursor-pointer"
            >
              <Archive className="w-4 h-4 mr-2" />
              Archived Sessions
            </DropdownMenuItem>
            <DropdownMenuSeparator className="bg-border-divider" />
            <DropdownMenuItem onClick={handleSignOut} className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover">
              <LogOut className="w-4 h-4 mr-2" />
              Sign Out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        </div>
      </div>
    </aside>
  )

  return (
    <div className="min-h-screen bg-background text-text-primary relative flex">
      
      {/* Desktop Sidebar */}
      <div className="hidden lg:block relative z-10 h-screen">
        {sidebar}
      </div>

      {/* Mobile Menu Button */}
      <button
        onClick={() => setIsMobileSidebarOpen(true)}
        className="lg:hidden fixed top-4 left-4 z-50 p-2 bg-surface-panel rounded-full text-text-primary hover:bg-interactive-hover transition-colors border-0"
      >
        <Menu className="w-6 h-6" />
      </button>

      {/* Mobile Sidebar Overlay */}
      {isMobileSidebarOpen && (
        <div className="lg:hidden fixed inset-0 z-50 flex">
          <div className="fixed inset-0 bg-background/80" onClick={() => setIsMobileSidebarOpen(false)} />
          <div className="relative flex-1 flex flex-col max-w-xs w-full h-screen">
            {sidebar}
            <button
              onClick={() => setIsMobileSidebarOpen(false)}
              className="absolute top-4 right-4 p-2 text-text-primary hover:text-text-primary"
            >
              <X className="w-6 h-6" />
            </button>
          </div>
        </div>
      )}

      {/* Main Content Area */}
      <main className="flex-1 bg-surface-panel relative z-10 flex flex-col">
        {/* Command Bar (if needed) */}
        {awaitingInputCount > 0 && !instanceId && (
          <div className="bg-surface-panel border-b border-border-divider px-4 sm:px-6 py-2">
            <div className="flex items-center justify-between">
              <span className="text-text-primary text-sm font-medium">
                <AlertCircle className="inline w-4 h-4 mr-1 text-text-secondary" />
                {awaitingInputCount === 1 
                  ? '1 chat awaiting your input' 
                  : `${awaitingInputCount} chats awaiting your input`}
              </span>
              <Button
                size="sm"
                variant="ghost"
                className="text-text-primary hover:text-text-primary hover:bg-interactive-hover rounded-full border-0 font-medium lowercase"
                onClick={() => {
                  const firstAwaitingInstance = agentGroups
                    .flatMap(g => g.instances)
                    .find(i => i.status === AgentStatus.AWAITING_INPUT)
                  if (firstAwaitingInstance) {
                    navigate(`/dashboard/instances/${firstAwaitingInstance.id}`)
                  }
                }}
              >
                view {awaitingInputCount === 1 ? '' : 'first'} →
              </Button>
            </div>
          </div>
        )}
        
        <div className="flex-1 overflow-auto flex flex-col">
          <div className={`flex flex-col h-full ${instanceId ? '' : 'px-6 py-6'}`}>
            <Outlet />
          </div>
        </div>
      </main>


      {/* Agent Selection Dialog */}
      <Dialog open={agentSelectionOpen} onOpenChange={setAgentSelectionOpen}>
        <DialogContent className="dialog-omnara max-w-md">
          <DialogHeader>
            <DialogTitle className="dialog-title-omnara">Select an Agent</DialogTitle>
            <DialogDescription className="text-cream/70">
              Choose an agent to start a new chat
            </DialogDescription>
          </DialogHeader>
          {userAgents && userAgents.length > 0 ? (
            <>
              <div className="space-y-2 py-4 max-h-[400px] overflow-y-auto">
                {userAgents.map((agent) => (
                  <button
                    key={agent.id}
                    onClick={() => handleAgentSelect(agent)}
                    className="w-full p-4 bg-warm-charcoal/50 hover:bg-warm-charcoal border border-cozy-amber/30 hover:border-cozy-amber/50 rounded-lg transition-all text-left group"
                  >
                    <div className="flex items-center justify-between">
                      <div className="flex items-center space-x-3">
                        <Bot className="w-5 h-5 text-soft-gold" />
                        <div>
                          <h4 className="text-cream font-medium">{agent.name}</h4>
                          <p className="text-cream/60 text-sm">
                            {agent.instance_count || 0} active instance{(agent.instance_count || 0) !== 1 ? 's' : ''}
                          </p>
                        </div>
                      </div>
                      {!agent.webhook_type && (
                        <div className="flex items-center space-x-1 text-cozy-amber">
                          <Link2 className="w-4 h-4" />
                          <span className="text-xs">Setup required</span>
                        </div>
                      )}
                    </div>
                  </button>
                ))}
              </div>
              <DialogFooter className="flex justify-between">
                <Button
                  onClick={() => {
                    setAgentSelectionOpen(false)
                    setCreateAgentDialogOpen(true)
                  }}
                  variant="outline"
                  className="bg-warm-charcoal/50 border-cozy-amber/30 text-cream hover:bg-cozy-amber/10 hover:text-soft-gold"
                >
                  <Plus className="w-4 h-4 mr-2" />
                  Create Agent
                </Button>
              </DialogFooter>
            </>
          ) : (
            <div className="flex items-center justify-center py-8">
              <div className="text-center">
                <p className="text-cream/60 mb-4">No agents configured yet</p>
                <Button
                  onClick={() => {
                    setAgentSelectionOpen(false)
                    setCreateAgentDialogOpen(true)
                  }}
                  className="retro-button text-warm-charcoal"
                  size="sm"
                >
                  <Plus className="w-4 h-4 mr-2" />
                  Create Agent
                </Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* Create Agent Dialog */}
      <Dialog open={createAgentDialogOpen} onOpenChange={setCreateAgentDialogOpen}>
        <DialogContent className="dialog-omnara max-w-md">
          <DialogHeader>
            <DialogTitle className="dialog-title-omnara">Create New Agent</DialogTitle>
            <DialogDescription className="text-cream/70">
              Enter a name for your new agent
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label className="text-cream">Agent Name</Label>
              <Input
                placeholder="e.g., My Claude Code Agent"
                value={newAgentName}
                onChange={(e) => setNewAgentName(e.target.value)}
                className="bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50"
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && newAgentName.trim()) {
                    createAgentMutation.mutate(newAgentName.trim())
                  }
                }}
              />
              <p className="text-xs text-cream/60">
                Choose a descriptive name for your agent
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setCreateAgentDialogOpen(false)
                setNewAgentName('')
              }}
              className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
            >
              Cancel
            </Button>
            <Button
              onClick={() => createAgentMutation.mutate(newAgentName.trim())}
              className="retro-button text-warm-charcoal"
              disabled={!newAgentName.trim() || createAgentMutation.isPending}
            >
              {createAgentMutation.isPending ? 'Creating...' : 'Create Agent'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Webhook Configuration Modal */}
      <WebhookConfigModal
        isOpen={webhookDialogOpen}
        onClose={() => {
          setWebhookDialogOpen(false)
          setSelectedAgentForWebhook(null)
        }}
        agentName={selectedAgentForWebhook?.name || ''}
        currentConfig={{
          webhook_type: selectedAgentForWebhook?.webhook_type,
          webhook_config: selectedAgentForWebhook?.webhook_config
        }}
        onSave={handleUpdateWebhook}
        isLoading={updateWebhookMutation.isPending}
      />

      {/* Launch Agent Modal */}
      <LaunchAgentModal
        isOpen={isPromptOpen}
        onClose={() => {
          setIsPromptOpen(false)
          setSelectedAgent(null)
        }}
        agent={selectedAgent}
        onLaunch={handleSubmitPrompt}
        onEditWebhook={() => {
          // Find the full agent data with webhook info
          const fullAgent = userAgents?.find(ua => ua.id === selectedAgent?.id) || selectedAgent
          // Close launch modal and open webhook setup
          setIsPromptOpen(false)
          setSelectedAgentForWebhook(fullAgent)
          setWebhookDialogOpen(true)
        }}
        isLoading={createInstanceMutation.isPending}
      />

      {/* Rename Dialog */}
      <Dialog open={renameDialogOpen} onOpenChange={setRenameDialogOpen}>
        <DialogContent className="dialog-omnara">
          <DialogHeader>
            <DialogTitle className="dialog-title-omnara">Rename Chat</DialogTitle>
            <DialogDescription className="text-cream/60">
              Enter a new name for this chat
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label className="text-cream">Chat Name</Label>
              <Input
                value={newInstanceName}
                onChange={(e) => setNewInstanceName(e.target.value)}
                placeholder="Enter chat name"
                className="bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50"
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    handleRenameSubmit()
                  }
                }}
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => setRenameDialogOpen(false)}
              className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
            >
              Cancel
            </Button>
            <Button
              onClick={handleRenameSubmit}
              className="retro-button text-warm-charcoal"
              disabled={!newInstanceName.trim() || renameInstanceMutation.isPending}
            >
              {renameInstanceMutation.isPending ? 'Renaming...' : 'Rename'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <AlertDialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <AlertDialogContent className="alert-omnara">
          <AlertDialogHeader>
            <AlertDialogTitle className="text-cream">Delete Chat</AlertDialogTitle>
            <AlertDialogDescription className="text-cream/60">
              Are you sure you want to delete this chat? This action cannot be undone.
              {instanceToDelete && (
                <div className="mt-2 p-3 bg-warm-charcoal/50 rounded-lg border border-cozy-amber/20">
                  <p className="text-cream text-sm font-medium">
                    {instanceToDelete.name || instanceToDelete.latest_message || 'Untitled Chat'}
                  </p>
                </div>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="bg-warm-charcoal/50 border-cozy-amber/20 text-cream hover:bg-cozy-amber/10 hover:text-cream">
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteConfirm}
              className="bg-terracotta hover:bg-terracotta/80 text-white"
              disabled={deleteInstanceMutation.isPending}
            >
              {deleteInstanceMutation.isPending ? 'Deleting...' : 'Delete'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Delete Agent Confirmation Dialog */}
      <AlertDialog open={deleteAgentDialogOpen} onOpenChange={setDeleteAgentDialogOpen}>
        <AlertDialogContent className="alert-omnara">
          <AlertDialogHeader>
            <AlertDialogTitle className="text-text-primary">Delete Agent</AlertDialogTitle>
            <AlertDialogDescription className="text-text-secondary">
              Are you sure you want to delete this agent? This will permanently delete the agent and all its associated chats. This action cannot be undone.
              {agentToDelete && (
                <div className="mt-2 p-3 bg-background rounded-lg border border-border-divider">
                  <p className="text-text-primary text-sm font-medium">
                    {agentToDelete.name}
                  </p>
                  <p className="text-text-secondary text-xs mt-1">
                    {agentToDelete.instances.length} chat{agentToDelete.instances.length !== 1 ? 's' : ''} will be deleted
                  </p>
                </div>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="bg-surface-panel border-border-divider text-text-primary hover:bg-interactive-hover">
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteAgentConfirm}
              className="bg-functional-negative hover:bg-functional-negative/80 text-white"
              disabled={deleteAgentMutation.isPending}
            >
              {deleteAgentMutation.isPending ? 'Deleting...' : 'Delete Agent'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Command Palette */}
      <CommandPalette
        isOpen={isCommandPaletteOpen}
        onClose={() => setIsCommandPaletteOpen(false)}
        onLaunchAgent={handleLaunchAgent}
      />

      {/* Archived Sessions Modal */}
      <Dialog open={archivedSessionsOpen} onOpenChange={setArchivedSessionsOpen}>
        <DialogContent className="bg-surface-panel border-border-divider max-w-4xl max-h-[80vh]">
          <DialogHeader>
            <DialogTitle className="text-text-primary flex items-center">
              <Archive className="w-5 h-5 mr-2" />
              Archived Sessions
            </DialogTitle>
            <DialogDescription className="text-text-secondary">
              View all completed and archived sessions across all agent types
            </DialogDescription>
          </DialogHeader>
          <div className="flex-1 overflow-y-auto py-4">
            {(() => {
              // Get all completed instances across all agent groups
              const allCompletedInstances = agentGroups.flatMap(group => 
                group.instances.filter(i => 
                  i.status === AgentStatus.COMPLETED || 
                  i.status === AgentStatus.FAILED || 
                  i.status === AgentStatus.KILLED
                )
              )
              
              // Group by agent type for display
              const groupedByAgent = agentGroups.map(group => ({
                ...group,
                completedInstances: group.instances.filter(i => 
                  i.status === AgentStatus.COMPLETED || 
                  i.status === AgentStatus.FAILED || 
                  i.status === AgentStatus.KILLED
                )
              })).filter(group => group.completedInstances.length > 0)
              
              if (allCompletedInstances.length === 0) {
                return (
                  <div className="text-center py-12">
                    <Archive className="w-12 h-12 text-text-secondary mx-auto mb-4" />
                    <p className="text-text-secondary">No archived sessions found</p>
                    <p className="text-text-secondary text-sm mt-1">
                      Completed sessions will appear here
                    </p>
                  </div>
                )
              }
              
              return (
                <div className="space-y-6">
                  {groupedByAgent.map(group => (
                    <div key={group.id}>
                      <h3 className="text-text-secondary text-sm font-medium mb-3 border-b border-border-divider pb-2">
                        {group.name} ({group.completedInstances.length} archived)
                      </h3>
                      <div className="space-y-2">
                        {group.completedInstances.map(instance => (
                          <div
                            key={instance.id}
                            className="group flex items-center space-x-3 p-3 rounded-lg hover:bg-interactive-hover transition-colors border border-border-divider"
                          >
                            <div className="w-6 flex justify-center">
                              {getArchivedListIcon(instance.status)}
                            </div>
                            <div className="flex-1 min-w-0">
                              <h4 className="text-text-primary text-sm font-medium truncate">
                                {getInstanceTitle(instance)}
                              </h4>
                              <p className="text-text-secondary text-xs mt-1">
                                Status: {instance.status} • ID: {instance.id.slice(-8)}
                              </p>
                            </div>
                            <div className="flex items-center space-x-2">
                              <Button
                                size="sm"
                                variant="ghost"
                                onClick={() => {
                                  setArchivedSessionsOpen(false)
                                  navigate(`/dashboard/instances/${instance.id}`)
                                }}
                                className="text-text-secondary hover:text-text-primary hover:bg-interactive-hover"
                              >
                                View
                              </Button>
                              <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="text-text-secondary hover:text-text-primary hover:bg-interactive-hover h-6 w-6 p-0"
                                  >
                                    <MoreHorizontal className="h-4 w-4" />
                                  </Button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align="end" className="bg-surface-panel border-border-divider">
                                  <DropdownMenuItem 
                                    onClick={() => {
                                      setArchivedSessionsOpen(false)
                                      handleRenameClick(instance)
                                    }}
                                    className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover"
                                  >
                                    <Edit2 className="w-4 h-4 mr-2" />
                                    Rename
                                  </DropdownMenuItem>
                                  <DropdownMenuItem 
                                    onClick={() => navigator.clipboard.writeText(instance.id)}
                                    className="text-text-primary hover:bg-interactive-hover focus:bg-interactive-hover"
                                  >
                                    <Copy className="w-4 h-4 mr-2" />
                                    Copy ID
                                  </DropdownMenuItem>
                                  <DropdownMenuSeparator className="bg-border-divider" />
                                  <DropdownMenuItem 
                                    onClick={() => {
                                      setArchivedSessionsOpen(false)
                                      handleDeleteClick(instance)
                                    }}
                                    className="text-functional-negative hover:bg-interactive-hover focus:bg-interactive-hover"
                                  >
                                    <Trash2 className="w-4 h-4 mr-2" />
                                    Delete
                                  </DropdownMenuItem>
                                </DropdownMenuContent>
                              </DropdownMenu>
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              )
            })()}
          </div>
          <DialogFooter>
            <Button
              onClick={() => setArchivedSessionsOpen(false)}
              className="bg-surface-panel hover:bg-interactive-hover text-text-primary border border-border-divider"
            >
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
