import { useState } from 'react'
import { RetroCard, RetroCardContent, RetroCardHeader, RetroCardTitle } from '@/components/ui/RetroCard'
import { Bot, Plus, Edit2, Trash2, ExternalLink } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { CozyEmptyState } from '@/components/ui/CozyEmptyState'

interface AgentData {
  id: string
  name: string
  webhook_url: string | null
  is_active: boolean
  created_at: string
  updated_at: string
  instance_count: number
  active_instance_count: number
  waiting_instance_count: number
  completed_instance_count: number
  error_instance_count: number
  webhook_type?: string | null
  webhook_config?: Record<string, any> | null
}

interface AgentCardProps {
  agent: AgentData
  onEdit: (agent: AgentData) => void
  onDelete: (agent: AgentData) => void
  onLaunch: (agent: AgentData) => void
}

function AgentCard({ agent, onEdit, onDelete, onLaunch }: AgentCardProps) {
  const navigate = useNavigate()
  
  const hasWebhook = !!agent.webhook_type
  
  // Calculate counts for status breakdown
  const totalInstances = agent.instance_count || 0
  const activeInstances = agent.active_instance_count || 0
  const waitingInstances = agent.waiting_instance_count || 0
  const completedInstances = agent.completed_instance_count || 0
  const errorInstances = agent.error_instance_count || 0
  
  // Check if all instances are completed
  const allCompleted = totalInstances > 0 && completedInstances === totalInstances
  
  // Calculate percentages for segmented bar
  const activePercentage = totalInstances > 0 ? (activeInstances / totalInstances) * 100 : 0
  const waitingPercentage = totalInstances > 0 ? (waitingInstances / totalInstances) * 100 : 0
  const errorPercentage = totalInstances > 0 ? (errorInstances / totalInstances) * 100 : 0
  const completedPercentage = totalInstances > 0 ? (completedInstances / totalInstances) * 100 : 0

  return (
    <div className="bg-warm-charcoal/60 rounded-lg hover:transform hover:scale-[1.02] transition-all duration-300 border border-cozy-amber/40 group">
      {/* Main Card Content */}
      <div className="p-3 sm:p-4">
        <div className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-3">
          {/* Left Section - Agent Info */}
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 sm:gap-3">
              <div className={`w-3 h-3 rounded-full flex-shrink-0 ${agent.is_active ? 'bg-sage-green' : 'bg-gray-500'}`} />
              
              <div
                className="flex-1 min-w-0 cursor-pointer"
                onClick={() => navigate(`/dashboard/user-agents/${agent.id}/instances`)}
              >
                <div className="flex items-center gap-2">
                  <h3 className="text-cream font-medium text-base sm:text-lg truncate group-hover:text-soft-gold transition-colors">{agent.name}</h3>
                  {hasWebhook && (
                    <span className="text-xs text-cozy-amber flex-shrink-0" title="Webhook configured">🔗</span>
                  )}
                </div>
                {agent.webhook_url && (
                  <div className="flex items-center gap-1 mt-1">
                    <ExternalLink className="h-3 w-3 text-cream/50 flex-shrink-0" />
                    <span className="text-xs text-cream/50 truncate">
                      {agent.webhook_url}
                    </span>
                  </div>
                )}
              </div>
            </div>
            
            {/* Status Pills */}
            <div className="flex flex-wrap items-center gap-1 sm:gap-2 mt-2 sm:mt-3">
              <span className="text-cream/70 text-xs sm:text-sm">{totalInstances} Total</span>
              
              {allCompleted ? (
                <span className="px-1.5 sm:px-2 py-0.5 sm:py-1 text-[10px] sm:text-xs font-medium bg-transparent text-gray-400 rounded-full border border-gray-400">
                  {completedInstances} Completed
                </span>
              ) : (
                <>
                  {activeInstances > 0 && (
                    <span className="px-1.5 sm:px-2 py-0.5 sm:py-1 text-[10px] sm:text-xs font-medium bg-sage-green/20 text-sage-green rounded-full border border-sage-green/30">
                      {activeInstances} Active
                    </span>
                  )}
                  {waitingInstances > 0 && (
                    <span className="px-1.5 sm:px-2 py-0.5 sm:py-1 text-[10px] sm:text-xs font-medium bg-cozy-amber/20 text-cozy-amber rounded-full border border-cozy-amber/30">
                      {waitingInstances} Waiting
                    </span>
                  )}
                  {errorInstances > 0 && (
                    <span className="px-1.5 sm:px-2 py-0.5 sm:py-1 text-[10px] sm:text-xs font-medium bg-terracotta/20 text-terracotta rounded-full border border-terracotta/30">
                      {errorInstances} Error
                    </span>
                  )}
                </>
              )}
            </div>
          </div>
          
          {/* Right Section - Actions */}
          <div className="flex items-center gap-1 sm:gap-2 flex-shrink-0">
            {hasWebhook && (
              <Button
                onClick={(e) => {
                  e.stopPropagation()
                  onLaunch(agent)
                }}
                size="sm"
                className="retro-button text-warm-charcoal text-xs sm:text-sm px-2 sm:px-3"
                title="Launch new instance"
              >
                <Plus className="h-3 w-3 sm:h-4 sm:w-4 mr-0.5 sm:mr-1" />
                <span className="hidden sm:inline">Launch</span>
                <span className="sm:hidden">+</span>
              </Button>
            )}
            
            <Button
              onClick={(e) => {
                e.stopPropagation()
                onEdit(agent)
              }}
              variant="ghost"
              size="sm"
              className="text-cream/70 hover:text-soft-gold hover:bg-cozy-amber/10 p-1.5 sm:p-2"
            >
              <Edit2 className="h-3 w-3 sm:h-4 sm:w-4" />
            </Button>
            
            <Button
              onClick={(e) => {
                e.stopPropagation()
                onDelete(agent)
              }}
              variant="ghost"
              size="sm"
              className="text-terracotta hover:text-terracotta/80 hover:bg-terracotta/10 p-1.5 sm:p-2"
            >
              <Trash2 className="h-3 w-3 sm:h-4 sm:w-4" />
            </Button>
          </div>
        </div>
        
        {/* Status Bar - Only show if not all completed */}
        {totalInstances > 0 && !allCompleted && (
          <div className="mt-2 sm:mt-3">
            <div 
              className="w-full bg-cream/10 rounded-full h-1 sm:h-1.5 overflow-hidden group"
              title={`Active: ${activeInstances}, Waiting: ${waitingInstances}, Error: ${errorInstances}`}
            >
              <div className="flex h-full">
                {activePercentage > 0 && (
                  <div 
                    className="bg-sage-green h-full transition-all duration-300"
                    style={{ width: `${activePercentage}%` }}
                  />
                )}
                {waitingPercentage > 0 && (
                  <div 
                    className="bg-cozy-amber h-full transition-all duration-300"
                    style={{ width: `${waitingPercentage}%` }}
                  />
                )}
                {errorPercentage > 0 && (
                  <div 
                    className="bg-terracotta h-full transition-all duration-300"
                    style={{ width: `${errorPercentage}%` }}
                  />
                )}
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

export function AgentManagementHub() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [selectedAgent, setSelectedAgent] = useState<AgentData | null>(null)
  const [isCreateOpen, setIsCreateOpen] = useState(false)
  const [isEditOpen, setIsEditOpen] = useState(false)
  const [isDeleteOpen, setIsDeleteOpen] = useState(false)
  const [isPromptOpen, setIsPromptOpen] = useState(false)
  
  // Form states
  const [formData, setFormData] = useState({
    name: '',
    webhook_url: '',
    webhook_api_key: '',
    is_active: true
  })
  
  const [prompt, setPrompt] = useState('')
  const [name, setName] = useState('')
  const [worktreeName, setWorktreeName] = useState('')

  // Fetch user agents
  const { data: userAgents, isLoading } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => apiClient.getUserAgents(),
    refetchInterval: 5000,
  })

  // Create user agent mutation
  const createAgentMutation = useMutation({
    mutationFn: (data: typeof formData) => apiClient.createUserAgent(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      toast.success('Agent configuration created successfully')
      setIsCreateOpen(false)
      resetForm()
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to create agent configuration')
    }
  })

  // Update user agent mutation
  const updateAgentMutation = useMutation({
    mutationFn: ({ id, data }: { id: string, data: typeof formData }) => 
      apiClient.updateUserAgent(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      toast.success('Agent configuration updated successfully')
      setIsEditOpen(false)
      resetForm()
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to update agent configuration')
    }
  })

  // Delete user agent mutation
  const deleteAgentMutation = useMutation({
    mutationFn: (id: string) => apiClient.deleteUserAgent(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      toast.success('Agent and all instances deleted successfully')
      setIsDeleteOpen(false)
      setSelectedAgent(null)
    },
    onError: (error: any) => {
      toast.error(error.message || 'Failed to delete agent')
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
        setIsPromptOpen(false)
        setPrompt('')
        setName('')
        setSelectedAgent(null)
        setPlanMode(false)
        
        if (data.agent_instance_id) {
          navigate(`/dashboard/instances/${data.agent_instance_id}`)
        }
      } else {
        toast.error(data.error || 'Failed to create agent instance')
      }
    },
    onError: (error: any) => {
      // Only show toast if apiClient hasn't already shown one
      if (!error.toastShown) {
        toast.error(error.message || 'Failed to create agent instance')
      }
    }
  })

  const resetForm = () => {
    setFormData({
      name: '',
      webhook_url: '',
      webhook_api_key: '',
      is_active: true
    })
    setSelectedAgent(null)
  }

  const handleEdit = (agent: AgentData) => {
    setSelectedAgent(agent)
    setFormData({
      name: agent.name,
      webhook_url: agent.webhook_url || '',
      webhook_api_key: '',
      is_active: agent.is_active
    })
    setIsEditOpen(true)
  }

  const handleDelete = (agent: AgentData) => {
    setSelectedAgent(agent)
    setIsDeleteOpen(true)
  }

  const handleLaunch = (agent: AgentData) => {
    setSelectedAgent(agent)
    setIsPromptOpen(true)
  }

  const handleSubmitAgent = () => {
    // Validation logic
    if (!selectedAgent) {
      const nameExists = userAgents?.some(
        (agent: AgentData) => agent.name.toLowerCase() === formData.name.toLowerCase()
      )
      if (nameExists) {
        toast.error(`An agent named '${formData.name}' already exists`)
        return
      }
    }
    
    if (selectedAgent) {
      const nameExists = userAgents?.some(
        (agent: AgentData) => 
          agent.id !== selectedAgent.id && 
          agent.name.toLowerCase() === formData.name.toLowerCase()
      )
      if (nameExists) {
        toast.error(`An agent named '${formData.name}' already exists`)
        return
      }
    }

    const submitData = {
      ...formData,
      webhook_url: formData.webhook_url.trim(),
    }

    if (selectedAgent) {
      updateAgentMutation.mutate({ id: selectedAgent.id, data: submitData })
    } else {
      createAgentMutation.mutate(submitData)
    }
  }

  const handleSubmitPrompt = () => {
    if (selectedAgent && prompt.trim()) {
      const finalPrompt = prompt
      
      createInstanceMutation.mutate({
        agentId: selectedAgent.id,
        prompt: finalPrompt,
        name: name.trim() || undefined,
        worktreeName: worktreeName.trim() || undefined
      })
    }
  }

  const confirmDelete = () => {
    if (selectedAgent) {
      deleteAgentMutation.mutate(selectedAgent.id)
    }
  }

  return (
    <>
      <RetroCard className="border-cozy-amber/30">
        <RetroCardHeader className="space-y-4">
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
            <RetroCardTitle className="flex items-center space-x-2">
              <Bot className="h-5 w-5 text-cozy-amber icon-hover" />
              <span className="text-lg sm:text-xl">Agent Management</span>
            </RetroCardTitle>
            
            <Button
              onClick={() => setIsCreateOpen(true)}
              className="retro-button text-warm-charcoal w-full sm:w-auto"
              size="sm"
            >
              <Plus className="h-4 w-4 mr-1" />
              New Agent
            </Button>
          </div>
          
          {/* Quick Stats */}
          {userAgents && userAgents.length > 0 && (
            <div className="grid grid-cols-3 gap-2 sm:gap-4">
              <div className="bg-warm-charcoal/60 rounded-lg p-2 sm:p-3 border border-cozy-amber/30">
                <p className="text-cream/60 text-[10px] sm:text-xs">Total Agents</p>
                <p className="text-cream text-lg sm:text-2xl font-semibold">{userAgents.length}</p>
              </div>
              <div className="bg-warm-charcoal/60 rounded-lg p-2 sm:p-3 border border-cozy-amber/30">
                <p className="text-cream/60 text-[10px] sm:text-xs">Total Instances</p>
                <p className="text-cream text-lg sm:text-2xl font-semibold">
                  {userAgents.reduce((sum: number, a: AgentData) => sum + a.instance_count, 0)}
                </p>
              </div>
              <div className="bg-warm-charcoal/60 rounded-lg p-2 sm:p-3 border border-cozy-amber/30">
                <p className="text-cream/60 text-[10px] sm:text-xs">Active Instances</p>
                <p className="text-cream text-lg sm:text-2xl font-semibold">
                  {userAgents.reduce((sum: number, a: AgentData) => sum + a.active_instance_count, 0)}
                </p>
              </div>
            </div>
          )}
        </RetroCardHeader>
        
        <RetroCardContent className="space-y-4">
          {isLoading ? (
            <CozyEmptyState
              variant="waiting-owl"
              title="Loading agents..."
              description="Fetching your agent configurations"
            />
          ) : userAgents?.length === 0 ? (
            <CozyEmptyState
              variant="sleeping-cat"
              title="No agents configured yet"
              description="Your AI agents are waiting to be summoned"
              action={
                <Button
                  onClick={() => setIsCreateOpen(true)}
                  className="retro-button text-warm-charcoal"
                >
                  <Plus className="h-4 w-4 mr-1" />
                  <span className="hidden sm:inline">Create Your First Agent</span>
                  <span className="sm:hidden">Create Agent</span>
                </Button>
              }
            />
          ) : (
            <div className="space-y-3">
              {userAgents?.map((agent: AgentData) => (
                <AgentCard
                  key={agent.id}
                  agent={agent}
                  onEdit={handleEdit}
                  onDelete={handleDelete}
                  onLaunch={handleLaunch}
                />
              ))}
            </div>
          )}
          
          {/* View All Instances link */}
          {userAgents && userAgents.length > 0 && (
            <div className="border-t border-cozy-amber/20 pt-4 mt-6">
              <button 
                onClick={() => navigate('/dashboard/instances')}
                className="w-full text-sm text-cream/70 hover:text-cream hover:bg-cozy-amber/10 hover:border-cozy-amber/70 font-medium flex items-center justify-center gap-2 py-2 rounded-lg border border-cozy-amber/30 transition-all duration-200"
              >
                View All Instances →
              </button>
            </div>
          )}
        </RetroCardContent>
      </RetroCard>

      {/* Create/Edit Agent Dialog */}
      <Dialog open={isCreateOpen || isEditOpen} onOpenChange={(open) => {
        if (!open) {
          setIsCreateOpen(false)
          setIsEditOpen(false)
          resetForm()
        }
      }}>
        <DialogContent className="dialog-omnara">
          <DialogHeader>
            <DialogTitle className="dialog-title-omnara">
              {selectedAgent ? 'Edit Agent Configuration' : 'Create New Agent'}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label className="text-cream">Agent Name</Label>
              <Input
                placeholder="My Custom Agent"
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                className="input-dark"
              />
            </div>
            
            <div className="space-y-2">
              <Label className="text-cream">Webhook URL</Label>
              <Input
                placeholder="https://your-agent.com/webhook"
                value={formData.webhook_url}
                onChange={(e) => setFormData({ ...formData, webhook_url: e.target.value })}
                className="input-dark"
              />
              <p className="text-xs text-cream/60">
                Add a webhook URL to enable remote triggering of this agent
              </p>
            </div>
            
            <div className="space-y-2">
              <Label className="text-cream">API Key</Label>
              <Input
                type="password"
                placeholder="Your webhook API key"
                value={formData.webhook_api_key}
                onChange={(e) => setFormData({ ...formData, webhook_api_key: e.target.value })}
                className="input-dark"
              />
              <p className="text-xs text-cream/60">
                This will be sent as a Bearer token in the Authorization header
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setIsCreateOpen(false)
                setIsEditOpen(false)
                resetForm()
              }}
              className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
            >
              Cancel
            </Button>
            <Button
              onClick={handleSubmitAgent}
              className="retro-button text-warm-charcoal"
              disabled={!formData.name || !formData.webhook_url || !formData.webhook_api_key}
            >
              {selectedAgent ? 'Update' : 'Create'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Launch Agent Dialog */}
      <Dialog open={isPromptOpen} onOpenChange={(open) => {
        if (!open) {
          setIsPromptOpen(false)
          setPrompt('')
          setName('')
          setWorktreeName('')
          setSelectedAgent(null)
        }
      }}>
        <DialogContent className="bg-warm-charcoal border-cozy-amber/30">
          <DialogHeader>
            <DialogTitle className="text-cream">
              Launch {selectedAgent?.name}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label className="text-cream">Initial Prompt</Label>
              <Textarea
                placeholder="What would you like the agent to do?"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                className="bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50 min-h-[100px]"
              />
              <p className="text-xs text-cream/60">
                This prompt will be sent to your agent to start the task
              </p>
            </div>
            
            <div className="space-y-2">
              <Label className="text-cream">Git Worktree (optional)</Label>
              <input
                type="text"
                placeholder="e.g., feature-auth, bugfix-login, or leave empty for new"
                value={worktreeName}
                onChange={(e) => {
                  const value = e.target.value
                  if (value === '' || /^[a-zA-Z0-9-_/]*$/.test(value)) {
                    setWorktreeName(value.slice(0, 100))
                  }
                }}
                className="w-full px-3 py-2 bg-warm-charcoal/50 border border-cozy-amber/20 text-cream placeholder:text-cream/50 rounded-md focus:outline-none focus:ring-2 focus:ring-cozy-amber focus:border-transparent"
              />
              <p className="text-xs text-cream/60">
                Specify a worktree name or leave empty to create a new timestamped worktree
              </p>
            </div>
            
            <div className="space-y-2">
              <Label className="text-cream">Branch Name (optional)</Label>
              <input
                type="text"
                placeholder="e.g., main, develop, feature/branch"
                value={name}
                onChange={(e) => {
                  const value = e.target.value
                  if (value === '' || /^[a-zA-Z0-9-_/]*$/.test(value)) {
                    setName(value.slice(0, 50))
                  }
                }}
                className="w-full px-3 py-2 bg-warm-charcoal/50 border border-cozy-amber/20 text-cream placeholder:text-cream/50 rounded-md focus:outline-none focus:ring-2 focus:ring-cozy-amber focus:border-transparent"
              />
              <p className="text-xs text-cream/60">
                Specify which branch to use in this worktree (defaults to current branch)
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setIsPromptOpen(false)
                setPrompt('')
                setName('')
                setSelectedAgent(null)
                    }}
              className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
            >
              Cancel
            </Button>
            <Button
              onClick={handleSubmitPrompt}
              className="retro-button text-warm-charcoal"
              disabled={!prompt.trim() || createInstanceMutation.isPending}
            >
              {createInstanceMutation.isPending ? 'Creating...' : 'Launch Agent'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={isDeleteOpen} onOpenChange={setIsDeleteOpen}>
        <DialogContent className="bg-warm-charcoal border-cozy-amber/30">
          <DialogHeader>
            <DialogTitle className="text-cream">Delete Agent</DialogTitle>
          </DialogHeader>
          <div className="py-4">
            <p className="text-cream mb-4">
              Are you sure you want to delete <span className="font-medium text-soft-gold">{selectedAgent?.name}</span>?
            </p>
            <div className="bg-terracotta/10 border border-terracotta/20 rounded-lg p-3">
              <p className="text-terracotta text-sm">
                <strong>Warning:</strong> This will permanently delete the agent configuration and all {selectedAgent?.instance_count || 0} associated instances. This action cannot be undone.
              </p>
            </div>
          </div>
          <DialogFooter className="flex-col-reverse sm:flex-row sm:space-x-2">
            <Button
              variant="ghost"
              onClick={() => setIsDeleteOpen(false)}
              className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10 w-full sm:w-auto"
            >
              Cancel
            </Button>
            <Button
              onClick={confirmDelete}
              className="bg-terracotta hover:bg-terracotta/80 text-warm-charcoal w-full sm:w-auto font-semibold"
              disabled={deleteAgentMutation.isPending}
            >
              {deleteAgentMutation.isPending ? 'Deleting...' : 'Delete Agent'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
