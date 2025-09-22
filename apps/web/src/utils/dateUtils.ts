export function getRelativeTimeString(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  
  // Reset time part for date comparison
  const dateDay = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const nowDay = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  
  const diffTime = nowDay.getTime() - dateDay.getTime()
  const diffDays = Math.floor(diffTime / (1000 * 60 * 60 * 24))
  
  if (diffDays === 0) {
    return 'Today'
  } else if (diffDays === 1) {
    return 'Yesterday'
  } else if (diffDays > 1 && diffDays < 7) {
    return `${diffDays} days ago`
  } else if (diffDays >= 7 && diffDays < 14) {
    return 'Last week'
  } else if (diffDays >= 14 && diffDays < 30) {
    return `${Math.floor(diffDays / 7)} weeks ago`
  } else if (diffDays >= 30 && diffDays < 365) {
    const months = Math.floor(diffDays / 30)
    return months === 1 ? 'Last month' : `${months} months ago`
  } else {
    // For older dates, show the formatted date
    return date.toLocaleDateString('en-US', { 
      month: 'short', 
      day: 'numeric',
      year: date.getFullYear() !== now.getFullYear() ? 'numeric' : undefined
    })
  }
}

export function getFullDateString(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString('en-US', {
    weekday: 'long',
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  })
}