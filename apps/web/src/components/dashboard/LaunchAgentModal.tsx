import React, { useState, useEffect } from 'react'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Edit2 } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'

interface WebhookField {
  name: string
  label: string
  type: 'string' | 'text' | 'password' | 'url' | 'select' | 'boolean' | 'number'
  required: boolean
  description?: string
  placeholder?: string
  default?: any
  validation_regex?: string
}

interface WebhookTypeSchema {
  id: string
  name: string
  build_fields: WebhookField[]
  runtime_fields: WebhookField[]
}

interface LaunchAgentModalProps {
  isOpen: boolean
  onClose: () => void
  agent: {
    id: string
    name: string
    webhook_type?: string
    webhook_config?: Record<string, any>
  } | null
  onLaunch: (agentId: string, runtimeData: Record<string, any>) => void
  onEditWebhook?: () => void
  isLoading?: boolean
}

export function LaunchAgentModal({
  isOpen,
  onClose,
  agent,
  onLaunch,
  onEditWebhook,
  isLoading = false
}: LaunchAgentModalProps) {
  const [runtimeValues, setRuntimeValues] = useState<Record<string, any>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})

  // Fetch webhook types to get runtime fields
  const { data: webhookTypes } = useQuery({
    queryKey: ['webhook-types'],
    queryFn: () => apiClient.getWebhookTypes(),
    enabled: isOpen && !!agent
  })

  // Get the webhook schema for this agent
  const webhookSchema = webhookTypes?.find((type: WebhookTypeSchema) => 
    type.id === (agent?.webhook_type || 'DEFAULT')
  )

  // Initialize runtime values when agent or schema changes
  useEffect(() => {
    if (webhookSchema && agent) {
      const newValues: Record<string, any> = {}
      
      webhookSchema.runtime_fields.forEach((field: WebhookField) => {
        if (field.default !== undefined && field.default !== null) {
          newValues[field.name] = field.default
        } else {
          newValues[field.name] = field.type === 'boolean' ? false : ''
        }
      })
      
      setRuntimeValues(newValues)
      setErrors({})
    }
  }, [agent, webhookSchema])

  const validateField = (field: WebhookField, value: any): string | null => {
    if (field.required && (!value || value === '')) {
      return `${field.label} is required`
    }

    if (field.validation_regex && value) {
      const regex = new RegExp(field.validation_regex)
      if (!regex.test(value)) {
        return `Invalid format for ${field.label}`
      }
    }

    return null
  }

  const handleFieldChange = (fieldName: string, value: any) => {
    setRuntimeValues(prev => ({ ...prev, [fieldName]: value }))
    
    // Clear error for this field
    if (errors[fieldName]) {
      setErrors(prev => {
        const newErrors = { ...prev }
        delete newErrors[fieldName]
        return newErrors
      })
    }
  }

  const handleLaunch = () => {
    if (!webhookSchema || !agent) return

    // Validate all runtime fields
    const newErrors: Record<string, string> = {}
    let hasErrors = false

    webhookSchema.runtime_fields.forEach((field: WebhookField) => {
      const error = validateField(field, runtimeValues[field.name])
      if (error) {
        newErrors[field.name] = error
        hasErrors = true
      }
    })

    if (hasErrors) {
      setErrors(newErrors)
      return
    }

    onLaunch(agent.id, runtimeValues)
  }

  const renderField = (field: WebhookField) => {
    const value = runtimeValues[field.name] ?? ''
    const error = errors[field.name]

    switch (field.type) {
      case 'text':
        return (
          <Textarea
            placeholder={field.placeholder}
            value={value}
            onChange={(e) => handleFieldChange(field.name, e.target.value)}
            className={`bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50 min-h-[100px] resize-y ${
              error ? 'border-red-500' : ''
            }`}
          />
        )

      case 'boolean':
        return (
          <label className="flex items-center space-x-2 cursor-pointer">
            <input
              type="checkbox"
              checked={value}
              onChange={(e) => handleFieldChange(field.name, e.target.checked)}
              className="rounded border-cozy-amber/30 bg-warm-charcoal/50 text-cozy-amber focus:ring-cozy-amber"
            />
            <span className="text-cream text-sm">{field.placeholder || `Enable ${field.label}`}</span>
          </label>
        )

      case 'password':
      case 'url':
      case 'string':
      case 'number':
      default:
        return (
          <Input
            type={field.type === 'password' ? 'password' : field.type === 'number' ? 'number' : 'text'}
            placeholder={field.placeholder}
            value={value}
            onChange={(e) => handleFieldChange(field.name, e.target.value)}
            className={`bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50 ${
              error ? 'border-red-500' : ''
            }`}
          />
        )
    }
  }

  const handleClose = () => {
    setRuntimeValues({})
    setErrors({})
    onClose()
  }

  return (
    <Dialog open={isOpen} onOpenChange={handleClose}>
      <DialogContent className="dialog-omnara max-w-3xl max-h-[80vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="dialog-title-omnara">
            Launch {agent?.name}
          </DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-4">
          {/* Webhook Info and Edit Button */}
          {agent?.webhook_type && onEditWebhook && (
            <div className="bg-warm-charcoal/30 border border-cozy-amber/20 rounded-lg px-3 py-2">
              <div className="flex items-center justify-between gap-3">
                <div className="flex-1">
                  <p className="text-sm font-medium text-cream/90">
                    Webhook Type: {webhookSchema?.name || agent.webhook_type || 'Custom'}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={onEditWebhook}
                  className="text-cream/70 hover:text-cream hover:bg-cozy-amber/10 flex-shrink-0"
                >
                  <Edit2 className="w-4 h-4" />
                </Button>
              </div>
            </div>
          )}

          {/* Dynamic Runtime Fields */}
          {webhookSchema && (
            <div className="space-y-4">
              {webhookSchema.runtime_fields.map((field: WebhookField) => (
                <div key={field.name} className="space-y-2">
                  <Label className="text-cream">
                    {field.label}
                    {field.required && <span className="text-cozy-amber ml-1">*</span>}
                  </Label>
                  {renderField(field)}
                  {field.description && !errors[field.name] && (
                    <p className="text-xs text-cream/60">{field.description}</p>
                  )}
                  {errors[field.name] && (
                    <p className="text-xs text-red-500">{errors[field.name]}</p>
                  )}
                </div>
              ))}
            </div>
          )}

        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={handleClose}
            className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
          >
            Cancel
          </Button>
          <Button
            onClick={handleLaunch}
            className="retro-button text-warm-charcoal"
            disabled={isLoading || !webhookSchema}
          >
            {isLoading ? 'Creating...' : 'Launch Agent'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
