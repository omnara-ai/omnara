
import { AgentStatus } from '../types/dashboard'

export function getStatusIcon(status: AgentStatus): string {
  switch (status) {
    case AgentStatus.ACTIVE:
      return '🟢'
    case AgentStatus.AWAITING_INPUT:
      return '🟡'
    case AgentStatus.PAUSED:
      return '⏸️'
    case AgentStatus.COMPLETED:
      return ''
    case AgentStatus.FAILED:
      return '❌'
    case AgentStatus.KILLED:
      return '⚫'
    default:
      return '❓'
  }
}

export function getStatusColor(status: AgentStatus): string {
  switch (status) {
    case AgentStatus.ACTIVE:
      return 'bg-green-500/20 border-green-400/30 text-green-200'
    case AgentStatus.AWAITING_INPUT:
      return 'bg-yellow-500/20 border-yellow-400/30 text-yellow-200'
    case AgentStatus.PAUSED:
      return 'bg-blue-500/20 border-blue-400/30 text-blue-200'
    case AgentStatus.COMPLETED:
      return 'bg-transparent border-gray-400 text-gray-400'
    case AgentStatus.FAILED:
      return 'bg-red-500/20 border-red-400/30 text-red-200'
    case AgentStatus.KILLED:
      return 'bg-gray-500/20 border-gray-400/30 text-gray-200'
    default:
      return 'bg-gray-500/20 border-gray-400/30 text-gray-200'
  }
}

export function getStatusLabel(status: AgentStatus, lastSignalAt?: string): string {
  switch (status) {
    case AgentStatus.ACTIVE:
      return 'Active'
    case AgentStatus.AWAITING_INPUT:
      return 'Waiting'
    case AgentStatus.PAUSED:
      return 'Paused by User'
    case AgentStatus.COMPLETED:
      return 'Completed'
    case AgentStatus.FAILED:
      return 'Failed'
    case AgentStatus.KILLED:
      return 'Killed by User'
    default:
      return 'Unknown'
  }
}

export function getTimeSince(dateString: string): string {
  try {
    // Parse the input date - handle both ISO strings with 'Z' and without
    const date = new Date(dateString)
    
    // Ensure we're working with UTC times consistently
    const now = new Date()
    
    // Debug logging to help identify the issue
    console.log('getTimeSince debug:', {
      dateString,
      parsedDate: date.toISOString(),
      now: now.toISOString(),
      dateTime: date.getTime(),
      nowTime: now.getTime()
    })
    
    // Check if date is valid
    if (isNaN(date.getTime())) {
      console.warn('Invalid date string:', dateString)
      return 'unknown'
    }
    
    // Calculate difference in milliseconds, then convert to seconds
    const diffInMs = now.getTime() - date.getTime()
    const diffInSeconds = Math.floor(diffInMs / 1000)
    
    console.log('getTimeSince time difference:', {
      diffInMs,
      diffInSeconds,
      diffInMinutes: Math.floor(diffInSeconds / 60),
      diffInHours: Math.floor(diffInSeconds / 3600)
    })
    
    // Handle negative values (future dates) - could happen with slight clock differences
    if (diffInSeconds < 0) {
      const absDiff = Math.abs(diffInSeconds)
      if (absDiff < 60) return 'just now'
      const absMinutes = Math.floor(absDiff / 60)
      if (absMinutes < 60) return `${absMinutes}m ago`
      const absHours = Math.floor(absMinutes / 60)
      if (absHours < 24) return `${absHours}h ago`
      const absDays = Math.floor(absHours / 24)
      return `${absDays}d ago`
    }
    
    // Very recent
    if (diffInSeconds < 10) return 'just now'
    if (diffInSeconds < 60) return `${diffInSeconds}s ago`
    
    const diffInMinutes = Math.floor(diffInSeconds / 60)
    if (diffInMinutes < 60) return `${diffInMinutes}m ago`
    
    const diffInHours = Math.floor(diffInMinutes / 60)
    if (diffInHours < 24) return `${diffInHours}h ago`
    
    const diffInDays = Math.floor(diffInHours / 24)
    return `${diffInDays}d ago`
  } catch (error) {
    console.error('Error formatting time:', error, dateString)
    return 'unknown'
  }
}

export function getDuration(startDate: string, endDate?: string): string {
  try {
    const start = new Date(startDate)
    const end = endDate ? new Date(endDate) : new Date()
    
    // console.log('getDuration debug:', {
    //   startDate,
    //   endDate: endDate || 'now',
    //   start: start.toISOString(),
    //   end: end.toISOString(),
    //   startTime: start.getTime(),
    //   endTime: end.getTime()
    // })
    
    if (isNaN(start.getTime()) || (endDate && isNaN(end.getTime()))) {
      return 'unknown'
    }
    
    const diffMs = Math.abs(end.getTime() - start.getTime())
    const diffInSeconds = Math.floor(diffMs / 1000)
    
    if (diffInSeconds < 60) return `${diffInSeconds}s`
    
    const diffInMinutes = Math.floor(diffInSeconds / 60)
    if (diffInMinutes < 60) return `${diffInMinutes}m`
    
    const diffInHours = Math.floor(diffInMinutes / 60)
    if (diffInHours < 24) return `${diffInHours}h`
    
    const diffInDays = Math.floor(diffInHours / 24)
    return `${diffInDays}d`
  } catch (error) {
    console.error('Error calculating duration:', error)
    return 'unknown'
  }
}


/**
 * Format agent type name for display (proper title case)
 * Handles any input case: "HELLO world" -> "Hello World"
 */
export function formatAgentTypeName(name: string): string {
  return name ? name.split(' ').map(w => w.charAt(0).toUpperCase() + w.slice(1).toLowerCase()).join(' ') : name
}
