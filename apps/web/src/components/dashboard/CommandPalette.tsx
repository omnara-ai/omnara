import { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Search, Home, Bot, Plus, Key, List, Settings, CreditCard, RotateCcw, Command, MessageSquare } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useQuery } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'
import { AgentInstance, AgentStatus } from '@/types/dashboard'
import { formatAgentTypeName } from '@/utils/statusUtils'

interface CommandItem {
  id: string
  type: 'navigation' | 'action' | 'chat'
  title: string
  description?: string
  icon: React.ReactNode
  action: () => void
  keywords?: string[]
}

interface CommandPaletteProps {
  isOpen: boolean
  onClose: () => void
  onLaunchAgent?: (agent: {id: string, name: string}) => void
}

export function CommandPalette({ isOpen, onClose, onLaunchAgent }: CommandPaletteProps) {
  const navigate = useNavigate()
  const [search, setSearch] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const itemsRef = useRef<(HTMLButtonElement | null)[]>([])

  // Fetch data for dynamic commands
  const { data: agentTypes } = useQuery({
    queryKey: ['agent-types'],
    queryFn: () => apiClient.getAgentTypes(),
    enabled: isOpen,
  })

  const { data: userAgents } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => apiClient.getUserAgents(),
    enabled: isOpen,
  })

  // Static commands
  const staticCommands: CommandItem[] = [
    {
      id: 'home',
      type: 'navigation',
      title: 'Go to Home',
      description: 'Navigate to Command Center',
      icon: <Home className="w-4 h-4" />,
      action: () => {
        navigate('/dashboard')
        onClose()
      },
      keywords: ['dashboard', 'command center', 'home']
    },
    {
      id: 'all-instances',
      type: 'navigation',
      title: 'View All Instances',
      description: 'See all chat instances',
      icon: <List className="w-4 h-4" />,
      action: () => {
        navigate('/dashboard/instances')
        onClose()
      },
      keywords: ['chats', 'conversations', 'all']
    },
    {
      id: 'api-keys',
      type: 'navigation',
      title: 'Manage API Keys',
      description: 'Configure API keys and webhooks',
      icon: <Key className="w-4 h-4" />,
      action: () => {
        navigate('/dashboard/api-keys')
        onClose()
      },
      keywords: ['api', 'keys', 'webhooks', 'configuration']
    },
    {
      id: 'settings',
      type: 'navigation',
      title: 'Settings',
      description: 'Account settings',
      icon: <Settings className="w-4 h-4" />,
      action: () => {
        navigate('/dashboard/settings')
        onClose()
      },
      keywords: ['account', 'preferences']
    },
    {
      id: 'billing',
      type: 'navigation',
      title: 'Billing',
      description: 'Manage billing and subscription',
      icon: <CreditCard className="w-4 h-4" />,
      action: () => {
        navigate('/dashboard/billing')
        onClose()
      },
      keywords: ['payment', 'subscription', 'plan']
    },
    {
      id: 'restart-onboarding',
      type: 'action',
      title: 'Restart Tutorial',
      description: 'Start the onboarding flow again',
      icon: <RotateCcw className="w-4 h-4" />,
      action: () => {
        localStorage.setItem('omnara_restart_onboarding', 'true')
        window.location.reload()
      },
      keywords: ['tutorial', 'onboarding', 'help', 'guide']
    }
  ]

  // Dynamic commands from data
  const dynamicCommands: CommandItem[] = []

  // Add launch agent commands
  if (userAgents) {
    userAgents.forEach(agent => {
      dynamicCommands.push({
        id: `launch-${agent.id}`,
        type: 'action',
        title: `Launch ${agent.name}`,
        description: `Start a new ${agent.name} instance`,
        icon: <Plus className="w-4 h-4" />,
        action: () => {
          if (onLaunchAgent) {
            onLaunchAgent(agent)
            onClose()
          }
        },
        keywords: ['new', 'create', 'start', agent.name.toLowerCase()]
      })
    })
  }

  // Add chat navigation commands
  if (agentTypes) {
    agentTypes.forEach(agentType => {
      agentType.recent_instances.forEach(instance => {
        const status = instance.status === AgentStatus.AWAITING_INPUT ? '⚠️' : 
                      instance.status === AgentStatus.ACTIVE ? '🟢' : ''
        
        dynamicCommands.push({
          id: `chat-${instance.id}`,
          type: 'chat',
          title: `${status} ${instance.latest_message || 'No messages yet'}`,
          description: formatAgentTypeName(agentType.name),
          icon: <MessageSquare className="w-4 h-4" />,
          action: () => {
            navigate(`/dashboard/instances/${instance.id}`)
            onClose()
          },
          keywords: [
            instance.name?.toLowerCase() || '',
            agentType.name.toLowerCase(),
            instance.latest_message?.toLowerCase() || ''
          ]
        })
      })
    })
  }

  const allCommands = [...staticCommands, ...dynamicCommands]

  // Filter commands based on search
  const filteredCommands = allCommands.filter(command => {
    const searchLower = search.toLowerCase()
    return (
      command.title.toLowerCase().includes(searchLower) ||
      command.description?.toLowerCase().includes(searchLower) ||
      command.keywords?.some(keyword => keyword.includes(searchLower))
    )
  })

  // Group commands by type
  const groupedCommands = filteredCommands.reduce((acc, command) => {
    if (!acc[command.type]) {
      acc[command.type] = []
    }
    acc[command.type].push(command)
    return acc
  }, {} as Record<string, CommandItem[]>)

  // Flatten for keyboard navigation
  const flatCommands = Object.entries(groupedCommands).flatMap(([_, commands]) => commands)

  // Reset selection when search changes
  useEffect(() => {
    setSelectedIndex(0)
  }, [search])

  // Focus input when opened
  useEffect(() => {
    if (isOpen) {
      setTimeout(() => {
        inputRef.current?.focus()
      }, 0)
      setSearch('')
      setSelectedIndex(0)
    }
  }, [isOpen])

  // Keyboard navigation
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (!isOpen) return

      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault()
          setSelectedIndex(i => Math.min(i + 1, flatCommands.length - 1))
          break
        case 'ArrowUp':
          e.preventDefault()
          setSelectedIndex(i => Math.max(i - 1, 0))
          break
        case 'Enter':
          e.preventDefault()
          if (flatCommands[selectedIndex]) {
            flatCommands[selectedIndex].action()
          }
          break
        case 'Escape':
          e.preventDefault()
          onClose()
          break
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [isOpen, selectedIndex, flatCommands, onClose])

  // Scroll selected item into view
  useEffect(() => {
    itemsRef.current[selectedIndex]?.scrollIntoView({
      block: 'nearest',
      behavior: 'smooth'
    })
  }, [selectedIndex])

  const getTypeLabel = (type: string) => {
    switch (type) {
      case 'navigation':
        return 'Navigation'
      case 'action':
        return 'Actions'
      case 'chat':
        return 'Chats'
      default:
        return type
    }
  }

  return (
    <Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent className="dialog-omnara p-0 max-w-2xl top-[20%] translate-y-0">
        <div className="border-b border-cozy-amber/20 p-4">
          <div className="flex items-center space-x-3">
            <Search className="w-5 h-5 text-cream/50 flex-shrink-0" />
            <input
              ref={inputRef}
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Type a command or search..."
              className="flex-1 bg-transparent text-cream placeholder:text-cream/50 focus:outline-none text-lg"
            />
            <kbd className="text-xs text-cream/50 bg-warm-charcoal px-2 py-1 rounded border border-cozy-amber/20">ESC</kbd>
          </div>
        </div>
        
        <div className="max-h-[400px] overflow-y-auto p-2">
          {Object.entries(groupedCommands).length === 0 ? (
            <div className="text-cream/50 text-sm p-8 text-center">
              No commands found matching "{search}"
            </div>
          ) : (
            Object.entries(groupedCommands).map(([type, commands], groupIndex) => (
              <div key={type} className={cn("mb-4", groupIndex > 0 && "mt-4")}>
                <div className="text-cream/50 text-xs uppercase tracking-wider px-2 mb-2">
                  {getTypeLabel(type)}
                </div>
                {commands.map((command, index) => {
                  const globalIndex = flatCommands.findIndex(c => c.id === command.id)
                  return (
                    <button
                      key={command.id}
                      ref={el => itemsRef.current[globalIndex] = el}
                      onClick={command.action}
                      className={cn(
                        "w-full text-left px-3 py-2 rounded-lg flex items-start space-x-3 transition-colors mb-1",
                        globalIndex === selectedIndex
                          ? "bg-cozy-amber/20 text-cream"
                          : "hover:bg-cozy-amber/10 text-cream/80"
                      )}
                    >
                      <div className="mt-0.5 text-soft-gold">
                        {command.icon}
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="font-medium truncate">{command.title}</div>
                        {command.description && (
                          <div className="text-xs text-cream/60 truncate mt-0.5">
                            {command.description}
                          </div>
                        )}
                      </div>
                      {command.type === 'action' && (
                        <kbd className="text-xs text-cream/50 bg-warm-charcoal px-2 py-1 rounded border border-cozy-amber/20">
                          <Command className="w-3 h-3 inline" />
                        </kbd>
                      )}
                    </button>
                  )
                })}
              </div>
            ))
          )}
        </div>
        
        <div className="border-t border-cozy-amber/20 p-3 flex items-center justify-between text-xs text-cream/50">
          <div className="flex items-center space-x-4">
            <span>
              <kbd className="px-1.5 py-0.5 bg-warm-charcoal rounded border border-cozy-amber/20">↑</kbd>
              <kbd className="px-1.5 py-0.5 bg-warm-charcoal rounded border border-cozy-amber/20 ml-1">↓</kbd>
              Navigate
            </span>
            <span>
              <kbd className="px-1.5 py-0.5 bg-warm-charcoal rounded border border-cozy-amber/20">↵</kbd>
              Select
            </span>
          </div>
          <span>
            {filteredCommands.length} result{filteredCommands.length !== 1 ? 's' : ''}
          </span>
        </div>
      </DialogContent>
    </Dialog>
  )
}
