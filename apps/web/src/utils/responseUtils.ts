// Extract the actual user answer from the full response text
export function extractUserAnswer(fullResponse: string): string {
  const omnaraPrefix = '\n------------------\nOmnara User Response:\n'
  const lastIndex = fullResponse.lastIndexOf(omnaraPrefix)
  
  if (lastIndex !== -1) {
    return fullResponse.substring(lastIndex + omnaraPrefix.length).trim()
  }
  
  // Fallback to full response if prefix not found
  return fullResponse
}