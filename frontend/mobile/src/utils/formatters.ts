/**
 * Format agent type name for display (proper title case)
 * Handles any input case: "HELLO world" -> "Hello World"
 */
export function formatAgentTypeName(name: string): string {
  if (!name || typeof name !== 'string') return '';
  return name.split(' ').map(w => w.charAt(0).toUpperCase() + w.slice(1).toLowerCase()).join(' ');
}

/**
 * Format time since for display
 */
export function formatTimeSince(timestamp: string): string {
  if (!timestamp || typeof timestamp !== 'string') return 'Unknown';
  
  try {
    const date = new Date(timestamp);
    if (isNaN(date.getTime())) return 'Unknown';
    
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    
    if (diffMins < 1) return 'Just now';
    if (diffMins < 60) return `${diffMins}m ago`;

    const diffHours = Math.floor(diffMins / 60);
    if (diffHours < 24) return `${diffHours}h ago`;

    const diffDays = Math.floor(diffHours / 24);
    return `${diffDays}d ago`;
  } catch (error) {
    return 'Unknown';
  }
}

/**
 * Get step count text
 */
export function getStepCountText(count: number): string {
  if (typeof count !== 'number' || isNaN(count) || count < 0) return '0 Steps';
  return count === 1 ? '1 Step' : `${count} Steps`;
}