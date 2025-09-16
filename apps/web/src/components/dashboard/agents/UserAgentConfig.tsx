import { useState } from 'react'
import { Plus, ExternalLink, Settings, Trash2 } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { toast } from 'sonner'

interface UserAgent {
  id: string
  name: string
  webhook_url?: string | null
  webhook_type?: string | null
  webhook_config?: Record<string, any> | null
  is_active: boolean
  created_at: string
  updated_at: string
  instance_count: number
  active_instance_count: number
}

export function UserAgentConfig() {
  const [isCreateOpen, setIsCreateOpen] = useState(false)
  const [isEditOpen, setIsEditOpen] = useState(false)
  const [isDeleteOpen, setIsDeleteOpen] = useState(false)
  const [selectedAgent, setSelectedAgent] = useState<UserAgent | null>(null)
  const [formData, setFormData] = useState({
    name: '',
    webhook_url: '',
    webhook_api_key: '',
    is_active: true
  })
  
  const queryClient = useQueryClient()

  // Fetch user agents
  const { data: userAgents, isLoading } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => apiClient.getUserAgents(),
  })

  // Create user agent mutation
  const createMutation = useMutation({
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
  const updateMutation = useMutation({
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
  const deleteMutation = useMutation({
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

  const resetForm = () => {
    setFormData({
      name: '',
      webhook_url: '',
      webhook_api_key: '',
      is_active: true
    })
    setSelectedAgent(null)
  }

  const handleEdit = (agent: UserAgent) => {
    setSelectedAgent(agent)
    setFormData({
      name: agent.name,
      webhook_url: agent.webhook_url || '',
      webhook_api_key: '', // Don't populate API key for security
      is_active: agent.is_active
    })
    setIsEditOpen(true)
  }

  const handleDelete = (agent: UserAgent) => {
    setSelectedAgent(agent)
    setIsDeleteOpen(true)
  }

  const confirmDelete = () => {
    if (selectedAgent) {
      deleteMutation.mutate(selectedAgent.id)
    }
  }

  const handleSubmit = () => {
    // Check for duplicate name when creating a new agent
    if (!selectedAgent) {
      const nameExists = userAgents?.some(
        (agent: UserAgent) => agent.name.toLowerCase() === formData.name.toLowerCase()
      )
      if (nameExists) {
        toast.error(`An agent named '${formData.name}' already exists`)
        return
      }
    }
    
    // Check for duplicate name when updating an agent (excluding current agent)
    if (selectedAgent) {
      const nameExists = userAgents?.some(
        (agent: UserAgent) => 
          agent.id !== selectedAgent.id && 
          agent.name.toLowerCase() === formData.name.toLowerCase()
      )
      if (nameExists) {
        toast.error(`An agent named '${formData.name}' already exists`)
        return
      }
    }

    // Prepare data with trimmed webhook URL
    const submitData = {
      ...formData,
      webhook_url: formData.webhook_url.trim(), // Trim whitespace from webhook URL
    }

    if (selectedAgent) {
      updateMutation.mutate({ id: selectedAgent.id, data: submitData })
    } else {
      createMutation.mutate(submitData)
    }
  }

  return (
    <Card className="bg-white/10 border-white/20 backdrop-blur-sm">
      <CardHeader>
        <CardTitle className="flex items-center justify-between text-white">
          <div className="flex items-center space-x-2">
            <Settings className="h-5 w-5" />
            <span>Your Agents</span>
          </div>
          <Button
            onClick={() => setIsCreateOpen(true)}
            className="bg-electric-accent hover:bg-electric-accent/80"
            size="sm"
          >
            <Plus className="h-4 w-4 mr-1" />
            New Agent
          </Button>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading ? (
          <div className="text-center py-8 text-off-white/60">Loading agents...</div>
        ) : userAgents?.length === 0 ? (
          <div className="text-center py-8 text-off-white/60">
            No agents configured. Click "New Agent" to get started.
          </div>
        ) : (
          <div className="space-y-3">
            {userAgents?.map((agent: UserAgent) => (
              <div
                key={agent.id}
                className="p-4 rounded-lg bg-white/5 hover:bg-white/10 transition-colors"
              >
                <div className="flex items-center justify-between">
                  <div className="flex items-center space-x-3">
                    <div className={`w-3 h-3 rounded-full ${agent.is_active ? 'bg-green-400' : 'bg-gray-400'}`} />
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-white font-medium">{agent.name}</span>
                        {agent.webhook_type && (
                          <span className="text-xs text-electric-accent" title="Webhook configured">🔗</span>
                        )}
                      </div>
                      {agent.webhook_url && (
                        <div className="flex items-center gap-1 mt-1">
                          <ExternalLink className="h-3 w-3 text-off-white/50" />
                          <span className="text-xs text-off-white/50 truncate max-w-[200px]">
                            {agent.webhook_url}
                          </span>
                        </div>
                      )}
                    </div>
                  </div>
                  <div className="flex items-center gap-4">
                    <div className="text-right">
                      <div className="text-sm text-off-white/70">
                        {agent.instance_count} instances
                      </div>
                      {agent.active_instance_count > 0 && (
                        <div className="text-xs text-green-400">
                          {agent.active_instance_count} active
                        </div>
                      )}
                    </div>
                    <div className="flex gap-2">
                      <Button
                        onClick={() => handleEdit(agent)}
                        variant="ghost"
                        size="sm"
                        className="text-off-white/70 hover:text-white"
                      >
                        Edit
                      </Button>
                      <Button
                        onClick={() => handleDelete(agent)}
                        variant="ghost"
                        size="sm"
                        className="text-red-400 hover:text-red-300"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      {/* Create/Edit Dialog */}
      <Dialog open={isCreateOpen || isEditOpen} onOpenChange={(open) => {
        if (!open) {
          setIsCreateOpen(false)
          setIsEditOpen(false)
          resetForm()
        }
      }}>
        <DialogContent className="bg-midnight-blue border-electric-blue/30">
          <DialogHeader>
            <DialogTitle className="text-white">
              {selectedAgent ? 'Edit Agent Configuration' : 'Create New Agent'}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label className="text-off-white">Agent Name</Label>
              <Input
                placeholder="My Custom Agent"
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                className="bg-white/10 border-electric-blue/20 text-white placeholder:text-white/50"
              />
            </div>
            
            <div className="space-y-2">
              <Label className="text-off-white">Webhook URL</Label>
              <Input
                placeholder="https://your-agent.com/webhook"
                value={formData.webhook_url}
                onChange={(e) => setFormData({ ...formData, webhook_url: e.target.value })}
                className="bg-white/10 border-electric-blue/20 text-white placeholder:text-white/50"
              />
              <p className="text-xs text-off-white/60">
                Add a webhook URL to enable remote triggering of this agent
              </p>
            </div>
            
            <div className="space-y-2">
              <Label className="text-off-white">API Key</Label>
              <Input
                type="password"
                placeholder="Your webhook API key"
                value={formData.webhook_api_key}
                onChange={(e) => setFormData({ ...formData, webhook_api_key: e.target.value })}
                className="bg-white/10 border-electric-blue/20 text-white placeholder:text-white/50"
              />
              <p className="text-xs text-off-white/60">
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
              className="text-off-white/80 hover:text-white hover:bg-white/10 transition-all duration-300"
            >
              Cancel
            </Button>
            <Button
              onClick={handleSubmit}
              className="bg-electric-accent hover:bg-electric-accent/80 text-white font-medium transition-all duration-300"
              disabled={!formData.name || !formData.webhook_url || !formData.webhook_api_key}
            >
              {selectedAgent ? 'Update' : 'Create'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={isDeleteOpen} onOpenChange={setIsDeleteOpen}>
        <DialogContent className="bg-dark-bg border-white/20">
          <DialogHeader>
            <DialogTitle className="text-white">Delete Agent</DialogTitle>
          </DialogHeader>
          <div className="py-4">
            <p className="text-off-white mb-4">
              Are you sure you want to delete <span className="font-medium text-white">{selectedAgent?.name}</span>?
            </p>
            <div className="bg-red-500/10 border border-red-500/20 rounded-lg p-3">
              <p className="text-red-400 text-sm">
                <strong>Warning:</strong> This will permanently delete the agent configuration and all {selectedAgent?.instance_count || 0} associated instances. This action cannot be undone.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => setIsDeleteOpen(false)}
              className="text-off-white/80 hover:text-white hover:bg-white/10"
            >
              Cancel
            </Button>
            <Button
              onClick={confirmDelete}
              className="bg-red-600 hover:bg-red-700 text-white"
              disabled={deleteMutation.isPending}
            >
              {deleteMutation.isPending ? 'Deleting...' : 'Delete Agent'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}