import React from 'react'
import { cn } from '@/lib/utils'

interface CozyEmptyStateProps {
  title?: string
  description?: string
  icon?: React.ReactNode
  action?: React.ReactNode
  className?: string
  variant?: 'sleeping-cat' | 'idle-robot' | 'starry-sky' | 'waiting-owl'
}

export function CozyEmptyState({ 
  title = "Nothing here yet",
  description = "When you have data, it will appear here",
  icon,
  action,
  className,
  variant = 'sleeping-cat'
}: CozyEmptyStateProps) {
  const getIllustration = () => {
    switch (variant) {
      case 'sleeping-cat':
        return (
          <div className="relative w-32 h-32 mx-auto mb-6">
            <div className="absolute inset-0 bg-cozy-amber/20 rounded-full blur-2xl" />
            <div className="relative">
              <span className="text-6xl">😴</span>
              <div className="absolute -bottom-2 -right-2 text-2xl animate-twinkle">✨</div>
            </div>
          </div>
        )
      case 'idle-robot':
        return (
          <div className="relative w-32 h-32 mx-auto mb-6">
            <div className="absolute inset-0 bg-dusty-rose/20 rounded-full blur-2xl" />
            <div className="relative">
              <span className="text-6xl">🤖</span>
              <div className="absolute -top-2 -right-2 text-xl">💤</div>
            </div>
          </div>
        )
      case 'starry-sky':
        return (
          <div className="relative w-32 h-32 mx-auto mb-6">
            <div className="absolute inset-0 bg-soft-gold/10 rounded-full blur-3xl" />
            <div className="relative flex items-center justify-center h-full">
              <span className="text-6xl">🌌</span>
              <div className="absolute top-0 left-0 text-lg animate-twinkle">⭐</div>
              <div className="absolute bottom-0 right-0 text-lg animate-twinkle" style={{ animationDelay: '1s' }}>✨</div>
              <div className="absolute top-1/2 right-0 text-sm animate-twinkle" style={{ animationDelay: '2s' }}>⭐</div>
            </div>
          </div>
        )
      case 'waiting-owl':
        return (
          <div className="relative w-32 h-32 mx-auto mb-6">
            <div className="absolute inset-0 bg-sage-green/20 rounded-full blur-2xl" />
            <div className="relative">
              <span className="text-6xl">🦉</span>
              <div className="absolute -bottom-3 left-1/2 -translate-x-1/2 text-xs font-mono text-cream/60">
                watching...
              </div>
            </div>
          </div>
        )
      default:
        return null
    }
  }

  return (
    <div 
      className={cn(
        "flex flex-col items-center justify-center py-12 px-6 text-center",
        className
      )}
    >
      {icon || getIllustration()}
      
      <h3 className="text-lg font-medium text-cream mb-2">
        {title}
      </h3>
      
      <p className="text-sm text-cream/60 max-w-sm mb-6">
        {description}
      </p>
      
      {action && (
        <div className="mt-2">
          {action}
        </div>
      )}
    </div>
  )
}