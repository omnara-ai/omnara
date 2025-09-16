export interface DiffFile {
  filename: string;
  additions: number;
  deletions: number;
  content: string;
}

export interface GitDiffSummary {
  files: DiffFile[];
  totalAdditions: number;
  totalDeletions: number;
}

export function parseGitDiff(gitDiff: string | null | undefined): GitDiffSummary | null {
  if (!gitDiff || gitDiff.trim() === '') {
    return null;
  }

  const files: DiffFile[] = [];
  let totalAdditions = 0;
  let totalDeletions = 0;

  // Split by file boundaries
  const fileChunks = gitDiff.split(/^diff --git /m).filter(chunk => chunk.trim());
  
  for (const chunk of fileChunks) {
    // Extract filename
    const fileMatch = chunk.match(/a\/(.+?) b\/(.+?)\n/);
    if (!fileMatch) continue;
    
    const filename = fileMatch[2];
    
    // Count additions and deletions
    const additions = (chunk.match(/^\+[^+]/gm) || []).length;
    const deletions = (chunk.match(/^-[^-]/gm) || []).length;
    
    files.push({
      filename,
      additions,
      deletions,
      content: `diff --git ${chunk}` // Restore the diff header
    });
    
    totalAdditions += additions;
    totalDeletions += deletions;
  }

  return {
    files,
    totalAdditions,
    totalDeletions
  };
}