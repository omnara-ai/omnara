import React, { useState, useEffect } from 'react'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Copy, AlertCircle } from 'lucide-react'
import { toast } from 'sonner'
import { apiClient } from '@/lib/dashboardApi'
import { useQuery } from '@tanstack/react-query'

interface WebhookField {
  name: string
  label: string
  type: 'string' | 'text' | 'password' | 'url' | 'select' | 'boolean' | 'number'
  required: boolean
  description?: string
  placeholder?: string
  default?: any
  options?: Array<{ label: string; value: string }>
  validation_regex?: string
  is_secret?: boolean
}

interface WebhookTypeSchema {
  id: string
  name: string
  description: string
  icon?: string
  build_fields: WebhookField[]  // Configuration fields
  runtime_fields: WebhookField[] // Runtime request fields
}

interface WebhookConfigModalProps {
  isOpen: boolean
  onClose: () => void
  agentName: string
  currentConfig?: {
    webhook_type?: string
    webhook_config?: Record<string, any>
  }
  onSave: (webhookType: string, webhookConfig: Record<string, any>) => void
  isLoading?: boolean
}

export function WebhookConfigModal({
  isOpen,
  onClose,
  agentName,
  currentConfig,
  onSave,
  isLoading = false
}: WebhookConfigModalProps) {
  const [selectedType, setSelectedType] = useState(currentConfig?.webhook_type || 'OMNARA_SERVE')
  const [fieldValues, setFieldValues] = useState<Record<string, any>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [isInitialized, setIsInitialized] = useState(false)

  // Fetch available webhook types from the backend
  const { data: webhookTypes, isLoading: typesLoading } = useQuery({
    queryKey: ['webhook-types'],
    queryFn: async () => {
      const types = await apiClient.getWebhookTypes()
      // Custom ordering: OMNARA_SERVE first, DEFAULT last
      return types.sort((a: WebhookTypeSchema, b: WebhookTypeSchema) => {
        if (a.id === 'OMNARA_SERVE') return -1
        if (b.id === 'OMNARA_SERVE') return 1
        if (a.id === 'DEFAULT') return 1
        if (b.id === 'DEFAULT') return -1
        return 0
      })
    },
    enabled: isOpen
  })

  // Get the current webhook type schema
  const currentSchema = webhookTypes?.find((type: WebhookTypeSchema) => type.id === selectedType)

  // Initialize form when modal opens or when schema becomes available
  useEffect(() => {
    if (currentSchema && isOpen && !isInitialized) {
      const newValues: Record<string, any> = {}
      
      // Initialize with defaults or existing values
      currentSchema.build_fields.forEach((field: WebhookField) => {
        if (currentConfig?.webhook_type === selectedType && currentConfig?.webhook_config?.[field.name] !== undefined) {
          // Use existing value if same type
          newValues[field.name] = currentConfig.webhook_config[field.name]
        } else if (field.default !== undefined && field.default !== null) {
          // Use default value
          newValues[field.name] = field.default
        } else {
          // Initialize with empty string for text fields
          newValues[field.name] = field.type === 'boolean' ? false : ''
        }
      })
      
      setFieldValues(newValues)
      setErrors({})
      setIsInitialized(true)
    }
  }, [currentSchema, isOpen, isInitialized, selectedType, currentConfig])

  // Reset initialization when modal closes or type changes
  useEffect(() => {
    if (!isOpen) {
      setIsInitialized(false)
      setErrors({})
    }
  }, [isOpen])

  // Handle webhook type change - this will trigger field reset
  const handleTypeChange = (newType: string) => {
    setSelectedType(newType)
    setIsInitialized(false) // Force reinitialization with new type
    setErrors({}) // Clear any validation errors
  }

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

    if (field.type === 'url' && value) {
      try {
        new URL(value)
      } catch {
        return 'Invalid URL format'
      }
    }

    return null
  }

  const handleFieldChange = (fieldName: string, value: any) => {
    setFieldValues(prev => ({ ...prev, [fieldName]: value }))
    
    // Clear error for this field
    if (errors[fieldName]) {
      setErrors(prev => {
        const newErrors = { ...prev }
        delete newErrors[fieldName]
        return newErrors
      })
    }
  }

  const handleSave = () => {
    if (!currentSchema) return

    // Validate all fields
    const newErrors: Record<string, string> = {}
    let hasErrors = false

    currentSchema.build_fields.forEach((field: WebhookField) => {
      const error = validateField(field, fieldValues[field.name])
      if (error) {
        newErrors[field.name] = error
        hasErrors = true
      }
    })

    if (hasErrors) {
      setErrors(newErrors)
      return
    }

    // Clean up the config (remove empty optional fields)
    const cleanedConfig = { ...fieldValues }
    currentSchema.build_fields.forEach((field: WebhookField) => {
      if (!field.required && (!cleanedConfig[field.name] || cleanedConfig[field.name] === '')) {
        delete cleanedConfig[field.name]
      }
    })

    onSave(selectedType, cleanedConfig)
  }

  const renderField = (field: WebhookField) => {
    const value = fieldValues[field.name] ?? ''
    const error = errors[field.name]

    switch (field.type) {
      case 'select':
        return (
          <Select
            value={value}
            onValueChange={(val) => handleFieldChange(field.name, val)}
          >
            <SelectTrigger className="bg-warm-charcoal/50 border-cozy-amber/20 text-cream">
              <SelectValue placeholder={field.placeholder || `Select ${field.label}`} />
            </SelectTrigger>
            <SelectContent className="bg-warm-charcoal border-cozy-amber/30">
              {field.options?.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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

      case 'text':
        return (
          <Textarea
            placeholder={field.placeholder}
            value={value}
            onChange={(e) => handleFieldChange(field.name, e.target.value)}
            className={cn(
              "bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50 min-h-[100px] resize-y",
              error && "border-red-500"
            )}
          />
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
            className={cn(
              "bg-warm-charcoal/50 border-cozy-amber/20 text-cream placeholder:text-cream/50",
              error && "border-red-500"
            )}
          />
        )
    }
  }

  // Helper function for className concatenation
  const cn = (...classes: (string | false | undefined)[]) => {
    return classes.filter(Boolean).join(' ')
  }

  // Check if all required fields are filled
  const isFormValid = () => {
    if (!currentSchema) return false
    
    for (const field of currentSchema.build_fields) {
      if (field.required) {
        const value = fieldValues[field.name]
        if (!value || value === '') {
          return false
        }
        // Additional validation for specific field types
        if (field.type === 'url' && value) {
          try {
            new URL(value)
          } catch {
            return false
          }
        }
        if (field.validation_regex && value) {
          const regex = new RegExp(field.validation_regex)
          if (!regex.test(value)) {
            return false
          }
        }
      }
    }
    return true
  }

  return (
    <Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent className="dialog-omnara max-w-2xl max-h-[80vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="dialog-title-omnara">
            Configure Webhook for {agentName}
          </DialogTitle>
          <DialogDescription className="text-cream/70">
            Select a webhook type and configure its settings
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          {/* Webhook Type Selection */}
          <div className="space-y-2">
            <Label className="text-cream">Webhook Type</Label>
            <Select
              value={selectedType}
              onValueChange={handleTypeChange}
              disabled={typesLoading}
            >
              <SelectTrigger className="bg-warm-charcoal/50 border-cozy-amber/20 text-cream">
                <SelectValue placeholder="Select webhook type">
                  {currentSchema?.name || selectedType}
                </SelectValue>
              </SelectTrigger>
              <SelectContent className="bg-warm-charcoal border-cozy-amber/30">
                {webhookTypes?.map((type: WebhookTypeSchema) => (
                  <SelectItem 
                    key={type.id} 
                    value={type.id}
                    className="text-cream hover:bg-cozy-amber/20 focus:bg-cozy-amber/20 data-[highlighted]:text-cream data-[highlighted]:bg-cozy-amber/20"
                  >
                    <div className="flex flex-col gap-0.5">
                      <span className="font-medium text-cream">{type.name}</span>
                      <span className="text-xs text-cream/50">{type.description}</span>
                    </div>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* Setup Instructions for Omnara Serve */}
          {selectedType === 'OMNARA_SERVE' && (
            <div className="bg-cozy-amber/10 border border-cozy-amber/30 rounded-lg p-4 space-y-3">
              <h4 className="text-sm font-medium text-cream">Quick Setup</h4>
              <p className="text-xs text-cream/80">
                Run this command to set up your webhook with Claude Code:
              </p>
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
              <p className="text-xs text-cream/60">
                This will display the webhook URL in your terminal. Copy it below.
              </p>
            </div>
          )}

          {/* Dynamic Build Fields Based on Schema */}
          {currentSchema && (
            <div className="space-y-4">
              {currentSchema.build_fields.map((field: WebhookField) => (
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
                    <p className="text-xs text-red-500 flex items-center gap-1">
                      <AlertCircle className="w-3 h-3" />
                      {errors[field.name]}
                    </p>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={onClose}
            className="text-cream/80 hover:text-cream hover:bg-cozy-amber/10"
          >
            Cancel
          </Button>
          <Button
            onClick={handleSave}
            className="retro-button text-warm-charcoal"
            disabled={isLoading || typesLoading || !currentSchema || !isFormValid()}
          >
            {isLoading ? 'Saving...' : 'Save Configuration'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
