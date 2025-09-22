export function scrubQuestionFormatMarkers(text: string): string {
  // Remove [YES/NO] marker
  let cleanedText = text.replace(/\n*\[YES\/NO\]\s*$/, '');
  
  // Remove [OPTIONS]...[/OPTIONS] block
  cleanedText = cleanedText.replace(/\n*\[OPTIONS\][\s\S]*?\[\/OPTIONS\]\s*$/, '');
  
  return cleanedText.trim();
}