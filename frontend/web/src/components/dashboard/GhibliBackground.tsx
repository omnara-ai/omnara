import React from 'react'

export function GhibliBackground() {
  return (
    <div className="fixed inset-0 -z-10 overflow-hidden">
      {/* Base gradient background - darker for better content focus */}
      <div className="absolute inset-0 bg-gradient-to-br from-warm-midnight via-deep-navy/90 to-warm-charcoal/85" />
      
      {/* Subtle warm base layer */}
      <div className="absolute inset-0 bg-gradient-to-t from-cozy-amber/3 to-transparent" />
      
      {/* Animated starfield */}
      <div className="absolute inset-0 starfield opacity-30" />
      
      {/* Soft aurora effects - more subtle */}
      <div className="absolute inset-0">
        <div className="absolute top-0 -left-1/4 w-1/2 h-1/2 bg-cozy-amber/8 rounded-full blur-3xl animate-aurora-drift-1" />
        <div className="absolute bottom-0 -right-1/4 w-1/2 h-1/2 bg-dusty-rose/8 rounded-full blur-3xl animate-aurora-drift-2" />
        <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-3/4 h-3/4 bg-soft-gold/5 rounded-full blur-3xl animate-aurora-drift-3" />
      </div>
      
      {/* Subtle film grain overlay */}
      <div className="absolute inset-0 film-grain opacity-15" />
      
      {/* Darker vignette effect for focus */}
      <div className="absolute inset-0 bg-vignette" />
      
      {/* Top gradient fade */}
      <div className="absolute inset-0 bg-top-fade" />
    </div>
  )
}
