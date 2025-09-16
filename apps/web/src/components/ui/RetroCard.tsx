import React from 'react'
import { cn } from '@/lib/utils'

interface RetroCardProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: 'default' | 'terminal' | 'highlight'
  glow?: boolean
  children: React.ReactNode
}

export function RetroCard({ 
  className, 
  variant = 'default', 
  glow = false,
  children, 
  ...props 
}: RetroCardProps) {
  return (
    <div
      className={cn(
        'cozy-card rounded-lg transition-all duration-300 hover-lift',
        {
          'default': 'bg-warm-charcoal/60',
          'terminal': 'bg-deep-navy/60 terminal-effect',
          'highlight': 'bg-warm-midnight/60'
        }[variant],
        glow && 'warm-glow',
        className
      )}
      {...props}
    >
      {children}
    </div>
  )
}

interface RetroCardHeaderProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode
}

export function RetroCardHeader({ className, children, ...props }: RetroCardHeaderProps) {
  return (
    <div 
      className={cn(
        'px-5 py-4 border-b border-cozy-amber/15',
        className
      )} 
      {...props}
    >
      {children}
    </div>
  )
}

interface RetroCardContentProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode
}

export function RetroCardContent({ className, children, ...props }: RetroCardContentProps) {
  return (
    <div 
      className={cn(
        'p-4',
        className
      )} 
      {...props}
    >
      {children}
    </div>
  )
}

interface RetroCardTitleProps extends React.HTMLAttributes<HTMLHeadingElement> {
  children: React.ReactNode
}

export function RetroCardTitle({ className, children, ...props }: RetroCardTitleProps) {
  return (
    <h3 
      className={cn(
        'text-lg font-semibold text-cream',
        className
      )} 
      {...props}
    >
      {children}
    </h3>
  )
}