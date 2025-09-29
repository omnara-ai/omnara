import { useState, useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { apiClient } from '@/lib/dashboardApi'
import { APIKey, NewAPIKey } from '@/types/dashboard'
import { Copy, Trash2, Plus, Eye, EyeOff } from 'lucide-react'
import { useAnalytics } from '@/lib/analytics'

export function APIKeyManagement() {
  const { track } = useAnalytics()
  const [apiKeys, setApiKeys] = useState<APIKey[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreateDialog, setShowCreateDialog] = useState(false)
  const [newKeyName, setNewKeyName] = useState('')
  const [expirationDays, setExpirationDays] = useState<number | ''>('')
  const [createdKey, setCreatedKey] = useState<NewAPIKey | null>(null)
  const [showCreatedKey, setShowCreatedKey] = useState(false)
  const [creating, setCreating] = useState(false)
  const [visibleKeys, setVisibleKeys] = useState<Set<string>>(new Set())
  
  // Count active API keys (revoked keys have is_active = false)
  const activeKeysCount = apiKeys.filter(key => key.is_active).length
  const hasReachedLimit = activeKeysCount >= 50

  const fetchAPIKeys = async () => {
    try {
      const response = await apiClient.getAPIKeys()
      setApiKeys(response)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch API keys')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchAPIKeys()
  }, [])

  const handleCreateKey = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newKeyName.trim()) return

    setCreating(true)
    try {
      const response = await apiClient.createAPIKey(
        newKeyName.trim(),
        expirationDays === '' ? null : expirationDays
      )

      setCreatedKey(response)
      setShowCreatedKey(true)
      setShowCreateDialog(false)

      // Track API key generation
      track('api_key_generated', {
        has_expiration: expirationDays !== '',
        expiration_days: expirationDays === '' ? null : expirationDays,
        source: 'api_keys_page'
      })

      setNewKeyName('')
      setExpirationDays('')
      await fetchAPIKeys()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create API key')
    } finally {
      setCreating(false)
    }
  }

  const handleRevokeKey = async (keyId: string) => {
    if (!confirm('Are you sure you want to revoke this API key? This action cannot be undone.')) {
      return
    }

    try {
      await apiClient.revokeAPIKey(keyId)
      await fetchAPIKeys()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to revoke API key')
    }
  }

  const copyToClipboard = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
    } catch (err) {
      console.error('Failed to copy to clipboard:', err)
    }
  }

  const toggleKeyVisibility = (keyId: string) => {
    setVisibleKeys(prev => {
      const newSet = new Set(prev)
      if (newSet.has(keyId)) {
        newSet.delete(keyId)
      } else {
        newSet.add(keyId)
      }
      return newSet
    })
  }

  const formatDate = (dateString: string) => {
    return new Date(dateString).toLocaleDateString('en-US', {
      year: 'numeric',
      month: 'short',
      day: 'numeric'
    })
  }

  const isExpired = (expiresAt: string | null) => {
    if (!expiresAt) return false  // Never expires
    return new Date(expiresAt) < new Date()
  }

  if (loading) {
    return (
      <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
        <CardContent className="flex items-center justify-center py-8">
          <div className="text-off-white/80 animate-fade-in">Loading API keys...</div>
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-6 animate-fade-in">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-3xl font-bold tracking-tight text-white bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent">
            API Keys
          </h2>
          <p className="text-off-white/80">
            Manage API keys for MCP (Model Context Protocol) authentication
          </p>
          <p className="text-sm text-off-white/60 mt-1">
            {activeKeysCount} / 50 active keys
          </p>
        </div>
        
        <Dialog open={showCreateDialog} onOpenChange={setShowCreateDialog}>
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <DialogTrigger asChild>
                  <Button 
                    className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0 disabled:opacity-50 disabled:cursor-not-allowed"
                    disabled={hasReachedLimit}
                  >
                    <Plus className="w-4 h-4 mr-2" />
                    Create API Key
                  </Button>
                </DialogTrigger>
              </TooltipTrigger>
              {hasReachedLimit && (
                <TooltipContent className="bg-midnight-blue/95 text-white border-white/20">
                  <p>You've reached the maximum of 50 API keys. Please delete some keys to create new ones.</p>
                </TooltipContent>
              )}
            </Tooltip>
          </TooltipProvider>
          <DialogContent className="bg-midnight-blue/95 backdrop-blur-md border border-white/20 text-white">
            <DialogHeader>
              <DialogTitle className="text-white bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent">
                Create New API Key
              </DialogTitle>
              <DialogDescription className="text-off-white/80">
                Generate a new API key for MCP client authentication. 
                This key will allow access to your agent data through MCP servers.
              </DialogDescription>
            </DialogHeader>
            <form onSubmit={handleCreateKey} className="space-y-4">
              <div>
                <label className="text-sm font-medium text-white">Key Name</label>
                <Input
                  placeholder="My MCP Client"
                  value={newKeyName}
                  onChange={(e) => setNewKeyName(e.target.value)}
                  required
                  className="bg-white/10 backdrop-blur-sm border-white/20 text-white placeholder:text-off-white/40 focus:ring-electric-accent focus:ring-offset-midnight-blue"
                />
              </div>
              <div>
                <label className="text-sm font-medium text-white">Expires in <span className="text-xs text-off-white/60">(days, optional)</span></label>
                <Input
                  type="number"
                  min="1"
                  max="999999"
                  value={expirationDays}
                  onChange={(e) => setExpirationDays(e.target.value === '' ? '' : parseInt(e.target.value))}
                  placeholder="30"
                  className="bg-white/10 backdrop-blur-sm border-white/20 text-white placeholder:text-off-white/40 focus:ring-electric-accent focus:ring-offset-midnight-blue"
                />
                <p className="text-xs text-off-white/70 mt-1">Leave empty for keys that never expire</p>
              </div>
              <div className="flex justify-end space-x-2">
                <Button 
                  type="button" 
                  variant="outline" 
                  onClick={() => setShowCreateDialog(false)}
                  className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                >
                  Cancel
                </Button>
                <Button 
                  type="submit" 
                  disabled={creating}
                  className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0"
                >
                  {creating ? 'Creating...' : 'Create Key'}
                </Button>
              </div>
            </form>
          </DialogContent>
        </Dialog>
      </div>

      {error && (
        <Card className="border border-red-400/30 bg-red-500/10 backdrop-blur-md">
          <CardContent className="pt-6">
            <div className="text-red-200">{error}</div>
          </CardContent>
        </Card>
      )}

      {/* Show newly created key */}
      {createdKey && (
        <Card className="border border-green-400/30 bg-green-500/10 backdrop-blur-md">
          <CardHeader>
            <CardTitle className="text-green-200">API Key Created Successfully!</CardTitle>
            <CardDescription className="text-green-300/80">
              Your new API key has been created successfully. You can view and copy it anytime from your API keys list.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div>
              <label className="text-sm font-medium text-white">API Key</label>
              <div className="flex space-x-2">
                <Textarea
                  value={showCreatedKey ? createdKey.api_key : '•'.repeat(createdKey.api_key.length)}
                  readOnly
                  className="font-mono text-sm bg-white/10 backdrop-blur-sm border-white/20 text-white resize-none min-h-0 h-auto"
                  style={{ height: 'auto', overflow: 'hidden' }}
                  onInput={(e) => {
                    e.currentTarget.style.height = 'auto'
                    e.currentTarget.style.height = e.currentTarget.scrollHeight + 'px'
                  }}
                  ref={(textarea) => {
                    if (textarea) {
                      textarea.style.height = 'auto'
                      textarea.style.height = textarea.scrollHeight + 'px'
                    }
                  }}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setShowCreatedKey(!showCreatedKey)}
                  className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                >
                  {showCreatedKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => copyToClipboard(createdKey.api_key)}
                  className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                >
                  <Copy className="w-4 h-4" />
                </Button>
              </div>
            </div>
            <Button 
              onClick={() => setCreatedKey(null)}
              className="w-full bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 border-0"
            >
              Got it
            </Button>
          </CardContent>
        </Card>
      )}

      <div className="grid gap-4">
        {apiKeys.filter(key => key.is_active).map((key, index) => (
          <Card 
            key={key.id} 
            className="border border-white/20 bg-white/10 backdrop-blur-md hover:bg-white/15 transition-all duration-300 animate-fade-in"
            style={{ animationDelay: `${index * 0.1}s` }}
          >
            <CardHeader>
              <div className="flex items-center justify-between">
                <div>
                  <CardTitle className="text-white">{key.name}</CardTitle>
                  <CardDescription className="text-off-white/70">
                    Created on {formatDate(key.created_at)}
                  </CardDescription>
                </div>
                <div className="flex items-center space-x-2">
                  {isExpired(key.expires_at) ? (
                    <Badge variant="destructive" className="bg-red-500/20 border-red-400/30 text-red-200">
                      Expired
                    </Badge>
                  ) : (
                    <Badge className="bg-green-500/20 border-green-400/30 text-green-200">
                      Active
                    </Badge>
                  )}
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <div className="space-y-3">
                {/* API Key Display */}
                <div>
                  <label className="text-sm font-medium text-white">API Key</label>
                  <div className="flex space-x-2">
                    <Textarea
                      value={visibleKeys.has(key.id) ? key.api_key : '•'.repeat(key.api_key.length)}
                      readOnly
                      className="font-mono text-sm bg-white/10 backdrop-blur-sm border-white/20 text-white resize-none min-h-0 h-auto"
                      style={{ height: 'auto', overflow: 'hidden' }}
                      onInput={(e) => {
                        e.currentTarget.style.height = 'auto'
                        e.currentTarget.style.height = e.currentTarget.scrollHeight + 'px'
                      }}
                      ref={(textarea) => {
                        if (textarea) {
                          textarea.style.height = 'auto'
                          textarea.style.height = textarea.scrollHeight + 'px'
                        }
                      }}
                    />
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => toggleKeyVisibility(key.id)}
                      className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                    >
                      {visibleKeys.has(key.id) ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => copyToClipboard(key.api_key)}
                      className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                    >
                      <Copy className="w-4 h-4" />
                    </Button>
                  </div>
                </div>
                
                {/* Key Details */}
                <div className="flex items-center justify-between">
                  <div className="space-y-1">
                    <div className="text-sm text-off-white/70">
                      Expires: {key.expires_at ? formatDate(key.expires_at) : 'Never'}
                    </div>
                    {key.last_used_at && (
                      <div className="text-sm text-off-white/70">
                        Last used: {formatDate(key.last_used_at)}
                      </div>
                    )}
                  </div>
                  {!isExpired(key.expires_at) && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleRevokeKey(key.id)}
                      className="border-red-400/30 text-red-200 bg-red-500/10 hover:bg-red-500/20 hover:text-red-100 transition-all duration-300"
                    >
                      <Trash2 className="w-4 h-4 mr-2" />
                      Delete
                    </Button>
                  )}
                </div>
              </div>
            </CardContent>
          </Card>
        ))}

        {apiKeys.length === 0 && (
          <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
            <CardContent className="flex items-center justify-center py-8">
              <div className="text-center space-y-2">
                <div className="text-off-white/70 text-lg">No API keys found</div>
                <div className="text-sm text-off-white/60">
                  Create your first API key to get started
                </div>
              </div>
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  )
} 