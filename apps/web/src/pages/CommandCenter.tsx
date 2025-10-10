
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'
import { HumanInputRequired, RecentActivity, KPICards, AgentManagementHub, OnboardingFlowSimple as OnboardingFlow } from '@/components/dashboard'
import { AgentType } from '../types/dashboard'
import { useState } from 'react'
import { useMobile } from '../hooks/use-mobile'
import { useNavigate } from 'react-router-dom'
import { Button } from '../components/ui/button'
import { Bot, Plus, Key, List, RotateCcw, AlertCircle, ArrowRight, Copy, Edit2, BookOpen } from 'lucide-react'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '../components/ui/dialog'
import { Label } from '../components/ui/label'
import { Textarea } from '../components/ui/textarea'
import { Input } from '../components/ui/input'
import { toast } from 'sonner'

// Helper function to normalize agent type names
const normalizeAgentTypeName = (name: string): string => {
  return name
    .split(' ')
    .map(word => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
    .join(' ')
}

// Helper function to consolidate agent types by normalized name
const consolidateAgentTypes = (agentTypes: AgentType[]): AgentType[] => {
  const consolidated = new Map<string, AgentType>()
  
  agentTypes.forEach(agentType => {
    const normalizedName = normalizeAgentTypeName(agentType.name)
    
    if (consolidated.has(normalizedName)) {
      // Merge instances from duplicate agent types
      const existing = consolidated.get(normalizedName)!
      consolidated.set(normalizedName, {
        ...existing,
        // Keep the first ID we encounter for the consolidated type
        recent_instances: [...existing.recent_instances, ...agentType.recent_instances]
      })
    } else {
      consolidated.set(normalizedName, {
        ...agentType,
        name: normalizedName
      })
    }
  })
  
  const result = Array.from(consolidated.values())
  
  return result
}

export default function CommandCenter() {
  const isMobile = useMobile()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [onboardingSkipped, setOnboardingSkipped] = useState(() => {
    // Check if onboarding was previously completed
    return localStorage.getItem('omnara_onboarding_completed') === 'true'
  })
  const [forceShowOnboarding, setForceShowOnboarding] = useState(() => {
    // Check if user explicitly requested to restart onboarding
    return localStorage.getItem('omnara_restart_onboarding') === 'true'
  })
  const [isPromptOpen, setIsPromptOpen] = useState(false)
  const [selectedAgent, setSelectedAgent] = useState<{id: string, name: string} | null>(null)
  const [prompt, setPrompt] = useState('')
  const [name, setName] = useState('')
  const [worktreeName, setWorktreeName] = useState('')
  
  // Webhook edit dialog state
  const [webhookDialogOpen, setWebhookDialogOpen] = useState(false)
  const [selectedAgentForWebhook, setSelectedAgentForWebhook] = useState<{id: string, name: string, webhook_url?: string, webhook_api_key?: string, is_active?: boolean} | null>(null)
  const [webhookUrl, setWebhookUrl] = useState('')
  const [webhookApiKey, setWebhookApiKey] = useState('')

  // Get recent activity data for timeline and human input
  const { data: agentTypes, isLoading: agentTypesLoading } = useQuery({
    queryKey: ['agent-types'],
    queryFn: () => apiClient.getAgentTypes(), // Get all instances per type for activity
    refetchInterval: 5000, // Poll every 5 seconds
  })

  // Get accurate counts for KPI cards
  const { data: agentSummary, isLoading: summaryLoading } = useQuery({
    queryKey: ['agent-summary'],
    queryFn: () => apiClient.getAgentSummary(),
    refetchInterval: 5000, // Poll every 5 seconds
  })

  // Get user agents for quick start cards
  const { data: userAgents } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => apiClient.getUserAgents(),
  })

  // Create instance mutation (must be before any conditional returns)
  const createInstanceMutation = useMutation({
    mutationFn: ({ agentId, prompt, name, worktreeName }: { agentId: string, prompt: string, name?: string, worktreeName?: string }) => 
      apiClient.createAgentInstance(agentId, prompt, name, worktreeName),
    onSuccess: (data) => {
      if (data.success) {
        toast.success('Agent instance created successfully')
        queryClient.invalidateQueries({ queryKey: ['user-agents'] })
        queryClient.invalidateQueries({ queryKey: ['agent-types'] })
        setIsPromptOpen(false)
        setPrompt('')
        setName('')
        setWorktreeName('')
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

  const handleLaunchAgent = (agent: {id: string, name: string}) => {
    setSelectedAgent(agent)
    setIsPromptOpen(true)
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

  // Webhook update mutation
  const updateWebhookMutation = useMutation({
    mutationFn: async ({ agentId, webhookUrl, webhookApiKey, name, isActive }: { 
      agentId: string, 
      webhookUrl: string,
      webhookApiKey: string,
      name: string,
      isActive: boolean 
    }) => {
      return apiClient.updateUserAgent(agentId, { 
        webhook_url: webhookUrl,
        webhook_api_key: webhookApiKey,
        name: name,
        is_active: isActive
      })
    },
    onSuccess: () => {
      toast.success('Webhook updated successfully')
      queryClient.invalidateQueries({ queryKey: ['user-agents'] })
      setWebhookDialogOpen(false)
      setSelectedAgentForWebhook(null)
      setWebhookUrl('')
      setWebhookApiKey('')
    },
    onError: (error: Error) => {
      toast.error(error.message || 'Failed to update webhook')
    }
  })

  const handleEditWebhook = (agent: any) => {
    setSelectedAgentForWebhook(agent)
    setWebhookUrl(agent.webhook_url || '')
    setWebhookApiKey(agent.webhook_api_key || '')
    setWebhookDialogOpen(true)
  }

  const handleUpdateWebhook = () => {
    if (selectedAgentForWebhook) {
      updateWebhookMutation.mutate({
        agentId: selectedAgentForWebhook.id,
        webhookUrl: webhookUrl.trim(),
        webhookApiKey: webhookApiKey.trim(),
        name: selectedAgentForWebhook.name,
        isActive: selectedAgentForWebhook.is_active ?? true
      })
    }
  }

  // Consolidate agent types to handle duplicates
  const consolidatedAgentTypes = agentTypes ? consolidateAgentTypes(agentTypes) : []

  // Get all instances that need input
  const instancesAwaitingInput = consolidatedAgentTypes?.flatMap(type => 
    type.recent_instances.filter(instance => instance.status === 'AWAITING_INPUT')
  ) || []

  // Use accurate counts from summary endpoint
  const activeAgents = agentSummary?.active_instances || 0
  const totalInstances = agentSummary?.total_instances || 0
  const tasksCompleted = agentSummary?.completed_instances || 0

  if (agentTypesLoading || summaryLoading) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <div className="text-soft-white/80 animate-fade-in">Loading Command Center...</div>
      </div>
    )
  }

  // Show onboarding flow if:
  // 1. User explicitly requested restart, OR
  // 2. No agent instances exist AND onboarding hasn't been completed
  const shouldShowOnboarding = forceShowOnboarding || 
    (!onboardingSkipped && 
     totalInstances === 0 && 
     (!consolidatedAgentTypes.length || consolidatedAgentTypes.every(type => type.recent_instances.length === 0)) &&
     !agentTypesLoading && !summaryLoading)

  const handleOnboardingComplete = () => {
    setOnboardingSkipped(true)
    setForceShowOnboarding(false)
    // Store onboarding completion in localStorage and clear restart flag
    localStorage.setItem('omnara_onboarding_completed', 'true')
    localStorage.removeItem('omnara_restart_onboarding')
    // Invalidate and refetch all queries to ensure fresh data
    queryClient.invalidateQueries({ queryKey: ['agent-types'] })
    queryClient.invalidateQueries({ queryKey: ['agent-summary'] })
  }

  if (shouldShowOnboarding) {
    return <OnboardingFlow onComplete={handleOnboardingComplete} />
  }

  return (
    <div className="max-w-4xl mx-auto space-y-8 animate-fade-in pb-12">
      {/* Welcome Section */}
      <div className="text-center pt-8 pb-4">
        <h1 className="retro-header text-4xl sm:text-5xl lg:text-6xl text-cream mb-4 glow-text">
          OMNARA <span style={{marginLeft:'-0.6em'}}>COMMAND</span> CENTER
        </h1>
        <p className="text-lg text-cream/80 max-w-2xl mx-auto">
          Your central hub for AI agent orchestration and collaboration
        </p>
        <div className="mt-6 flex flex-col items-center space-y-2">
          <span className="text-cream/80 text-sm">Get started with:</span>
          <button
            onClick={() => {
              navigator.clipboard.writeText('pip install omnara && omnara')
              toast.success('Command copied to clipboard!')
            }}
            className="group flex items-center space-x-3 px-6 py-3 bg-warm-charcoal/50 border border-cozy-amber/30 rounded-lg hover:border-cozy-amber/50 hover:bg-warm-charcoal/70 transition-all duration-200"
          >
            <code className="text-lg font-mono text-cozy-amber">pip install omnara && omnara</code>
            <Copy className="w-4 h-4 text-cream/50 group-hover:text-cozy-amber transition-colors" />
          </button>
          <div className="text-cream/50 text-xs">
            or with uv:
            <button
              onClick={() => {
                navigator.clipboard.writeText('uv pip install omnara && uv run omnara')
                toast.success('Command copied to clipboard!')
              }}
              className="ml-2 text-cream/60 hover:text-cozy-amber transition-colors"
            >
              <code className="font-mono">uv pip install omnara && uv run omnara</code>
              <Copy className="w-3 h-3 inline ml-1 mb-0.5" />
            </button>
          </div>
          <div className="mt-3">
            <a
              href="https://omnara.mintlify.dev/"
              target="_blank"
              rel="noopener noreferrer"
              className="text-sm text-cream/60 hover:text-cozy-amber transition-colors inline-flex items-center space-x-1"
            >
              <BookOpen className="w-3.5 h-3.5" />
              <span>See full documentation</span>
            </a>
          </div>
        </div>
      </div>

      {/* Command Bar Hint */}
      <div className="panel-omnara p-4 flex items-center justify-between">
        <div className="flex items-center space-x-3">
          <div className="p-2 bg-cozy-amber/20 rounded-lg">
            <svg className="w-5 h-5 text-soft-gold" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 10V3L4 14h7v7l9-11h-7z" />
            </svg>
          </div>
          <div>
            <p className="text-cream font-medium">Quick Command</p>
            <p className="text-cream/60 text-sm">Press <kbd className="kbd-chip">⌘K</kbd> to search or run commands</p>
          </div>
        </div>
      </div>

      {/* Awaiting Input Summary */}
      {instancesAwaitingInput.length > 0 && (
        <div className="bg-cozy-amber/25 border-2 border-cozy-amber/50 rounded-lg p-6 shadow-lg backdrop-blur-sm">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-lg font-semibold text-cream flex items-center space-x-2">
              <AlertCircle className="w-5 h-5 text-cozy-amber" />
              <span>Awaiting Your Input</span>
            </h2>
            <span className="chip-amber">
              {instancesAwaitingInput.length} chat{instancesAwaitingInput.length !== 1 ? 's' : ''}
            </span>
          </div>
          <div className="space-y-2">
            {instancesAwaitingInput.slice(0, 3).map(instance => (
              <button
                key={instance.id}
                onClick={() => navigate(`/dashboard/instances/${instance.id}`)}
                className="w-full text-left p-3 bg-warm-charcoal/80 hover:bg-warm-charcoal border border-cozy-amber/30 hover:border-cozy-amber/50 rounded-lg transition-all shadow-md hover:shadow-lg group"
              >
                <div className="flex items-center justify-between">
                  <div className="flex-1 min-w-0">
                    <h4 className="text-cream text-sm font-medium truncate">
                      {instance.latest_message || 'Waiting for your response...'}
                    </h4>
                  </div>
                  <ArrowRight className="w-4 h-4 text-soft-gold flex-shrink-0 ml-2 group-hover:translate-x-1 transition-transform" />
                </div>
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Quick Start Cards */}
      <div>
        <h2 className="text-lg font-semibold text-cream mb-4">Quick Start</h2>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {userAgents && userAgents.length > 0 ? (
            userAgents.slice(0, 4).map(agent => (
              <div
                key={agent.id}
                className="panel-omnara p-6"
              >
                <div className="flex items-center justify-between mb-2">
                  <Bot className="w-5 h-5 text-soft-gold" />
                  <div className="flex items-center space-x-2">
                    <button
                      onClick={() => handleLaunchAgent(agent)}
                      className="icon-btn-quiet"
                      title="Launch new instance"
                    >
                      <Plus className="w-4 h-4" />
                    </button>
                    <button
                      onClick={() => handleEditWebhook(agent)}
                      className="icon-btn-quiet"
                      title="Edit webhook"
                    >
                      <Edit2 className="w-4 h-4" />
                    </button>
                  </div>
                </div>
                <h3 className="text-cream font-medium mb-1">{agent.name}</h3>
                <p className="text-cream/60 text-sm">
                  {agent.instance_count} instance{agent.instance_count !== 1 ? 's' : ''} • 
                  {agent.active_instance_count} active
                </p>
              </div>
            ))
          ) : (
            <button
              onClick={() => navigate('/dashboard/api-keys')}
              className="panel-omnara p-6 text-left col-span-2"
            >
              <div className="flex items-center space-x-3">
                <div className="p-3 bg-cozy-amber/20 rounded-lg">
                  <Key className="w-6 h-6 text-soft-gold" />
                </div>
                <div>
                  <h3 className="text-cream font-medium mb-1">Configure Your First Agent</h3>
                  <p className="text-cream/60 text-sm">
                    Set up API keys and webhooks to start using AI agents
                  </p>
                </div>
              </div>
            </button>
          )}
        </div>
      </div>

      {/* Stats Summary - Smaller and more subtle */}
      <div className="grid grid-cols-3 gap-3">
        <div className="stat-tile p-3 text-center">
          <p className="text-2xl font-semibold text-soft-gold">{totalInstances}</p>
          <p className="text-cream/50 text-xs">Total Chats</p>
        </div>
        <div className="stat-tile p-3 text-center">
          <p className="text-2xl font-semibold text-sage-green">{activeAgents}</p>
          <p className="text-cream/50 text-xs">Active Now</p>
        </div>
        <div className="stat-tile p-3 text-center">
          <p className="text-2xl font-semibold text-cream">{tasksCompleted}</p>
          <p className="text-cream/50 text-xs">Completed</p>
        </div>
      </div>

      {/* Quick Actions */}
      <div className="flex flex-col sm:flex-row gap-4">
        <Button
          onClick={() => navigate('/dashboard/instances')}
          variant="outline"
          className="btn-omnara-ghost flex-1"
        >
          <List className="w-4 h-4 mr-2" />
          View All Instances
        </Button>
        <Button
          onClick={() => {
            localStorage.setItem('omnara_restart_onboarding', 'true')
            window.location.reload()
          }}
          variant="outline"
          className="btn-omnara-ghost flex-1"
        >
          <RotateCcw className="w-4 h-4 mr-2" />
          Restart Tutorial
        </Button>
      </div>

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
        <DialogContent className="dialog-omnara">
          <DialogHeader>
            <DialogTitle className="dialog-title-omnara">
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
                className="textarea-dark min-h-[100px]"
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
                className="input-dark w-full px-3 py-2"
              />
              <p className="text-xs text-cream/60">
                Specify a worktree name or leave empty to create a new timestamped worktree
              </p>
            </div>
            
            <div className="space-y-2">
              <Label className="text-cream">Branch Name (optional)</Label>
              <input
                type="text"
                placeholder="e.g., main, develop, feature-branch"
                value={name}
                onChange={(e) => {
                  const value = e.target.value
                  if (value === '' || /^[a-zA-Z0-9-_/]*$/.test(value)) {
                    setName(value.slice(0, 50))
                  }
                }}
                className="input-dark w-full px-3 py-2"
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
                setWorktreeName('')
                setSelectedAgent(null)
                      }}
              className="btn-omnara-ghost"
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

      {/* Webhook Edit Dialog */}
      <Dialog open={webhookDialogOpen} onOpenChange={setWebhookDialogOpen}>
        <DialogContent className="dialog-omnara max-w-2xl">
          <DialogHeader>
            <DialogTitle className="dialog-title-omnara">
              Edit Webhook for {selectedAgentForWebhook?.name}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            {/* Setup Instructions */}
            <div className="bg-cozy-amber/10 border border-cozy-amber/30 rounded-lg p-4 space-y-3">
              <h4 className="text-sm font-medium text-cream">Quick Setup</h4>
              <p className="text-xs text-cream/80">
                Run these commands to set up your webhook with Claude Code:
              </p>
              <div className="space-y-2">
                <div className="flex items-center space-x-2">
                  <code className="code-chip flex-1">
                    brew install pipx
                  </code>
                  <button
                    onClick={() => {
                      navigator.clipboard.writeText('brew install pipx')
                      toast.success('Command copied!')
                    }}
                    className="p-1 text-cream/50 hover:text-cozy-amber transition-colors"
                  >
                    <Copy className="w-3 h-3" />
                  </button>
                </div>
                <div className="flex items-center space-x-2">
                  <code className="code-chip flex-1">
                    omnara serve
                  </code>
                  <button
                    onClick={() => {
                      navigator.clipboard.writeText('omnara serve')
                      toast.success('Command copied!')
                    }}
                    className="p-1 text-cream/50 hover:text-cozy-amber transition-colors"
                  >
                    <Copy className="w-3 h-3" />
                  </button>
                </div>
              </div>
              <p className="text-xs text-cream/60">
                This will display the webhook URL and API key in your terminal. Copy them below:
              </p>
            </div>

            <div className="space-y-2">
              <Label className="text-cream">Webhook URL</Label>
              <Input
                type="url"
                placeholder="https://your-webhook-url.com"
                value={webhookUrl}
                onChange={(e) => setWebhookUrl(e.target.value.trim())}
                className="input-dark"
              />
              <p className="text-xs text-cream/60">
                Enter the webhook URL from the terminal output
              </p>
            </div>
            
            <div className="space-y-2">
              <Label className="text-cream">Webhook API Key</Label>
              <Input
                type="password"
                placeholder="Enter API key for authentication"
                value={webhookApiKey}
                onChange={(e) => setWebhookApiKey(e.target.value.trim())}
                className="input-dark"
              />
              <p className="text-xs text-cream/60">
                Enter the API key from the terminal output
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setWebhookDialogOpen(false)
                setSelectedAgentForWebhook(null)
                setWebhookUrl('')
                setWebhookApiKey('')
              }}
              className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
            >
              Cancel
            </Button>
            <Button
              onClick={handleUpdateWebhook}
              className="retro-button text-warm-charcoal"
              disabled={updateWebhookMutation.isPending}
            >
              {updateWebhookMutation.isPending ? 'Updating...' : 'Update Webhook'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
