export type QuestionFormat = 'yes-no' | 'options' | 'open-ended';

export interface ParsedQuestion {
  format: QuestionFormat;
  questionText: string;
  options?: string[];
}

export function parseQuestionFormat(text: string): ParsedQuestion {
  // Check for YES/NO format
  const yesNoRegex = /^([\s\S]*?)\n*\[YES\/NO\]\s*$/;
  const yesNoMatch = text.match(yesNoRegex);
  
  if (yesNoMatch) {
    return {
      format: 'yes-no',
      questionText: yesNoMatch[1].trim(),
    };
  }
  
  // Check for OPTIONS format
  const optionsRegex = /^([\s\S]*?)\n*\[OPTIONS\]\n*([\s\S]+?)\n*\[\/OPTIONS\]\s*$/;
  const optionsMatch = text.match(optionsRegex);
  
  if (optionsMatch) {
    const questionText = optionsMatch[1].trim();
    const optionsText = optionsMatch[2].trim();
    
    // Parse options (numbered list)
    const options = optionsText
      .split('\n')
      .map(line => line.trim())
      .filter(line => /^\d+\.\s+/.test(line))
      .map(line => line.replace(/^\d+\.\s+/, ''));
    
    return {
      format: 'options',
      questionText,
      options,
    };
  }
  
  // Default to open-ended
  return {
    format: 'open-ended',
    questionText: text.trim(),
  };
}