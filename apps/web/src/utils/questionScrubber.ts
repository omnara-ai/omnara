/**
 * Removes structured format markers from question text
 * This is used to clean up questions for display in timeline and other UI components
 */
export function scrubQuestionFormatMarkers(text: string): string {
  // Remove [YES/NO] marker at the end
  let cleanedText = text.replace(/\n*\[YES\/NO\]\s*$/, '')
  
  // Remove [OPTIONS]...[/OPTIONS] block at the end
  cleanedText = cleanedText.replace(/\n*\[OPTIONS\][\s\S]*?\[\/OPTIONS\]\s*$/, '')
  
  // Trim any trailing whitespace
  return cleanedText.trim()
}