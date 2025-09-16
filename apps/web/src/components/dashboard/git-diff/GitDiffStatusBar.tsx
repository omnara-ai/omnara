import { useMemo } from 'react'
import { GitBranch } from 'lucide-react'
import { parseGitDiff } from '@/utils/gitDiffParser'
import { cn } from '@/lib/utils'

interface GitDiffStatusBarProps {
  gitDiff: string | null | undefined
  onClick: () => void
  className?: string
}

export function GitDiffStatusBar({ gitDiff, onClick, className }: GitDiffStatusBarProps) {
  const diffSummary = useMemo(() => parseGitDiff(gitDiff), [gitDiff])
  
  if (!diffSummary || diffSummary.files.length === 0) {
    return null
  }

  return (
    <button
      onClick={onClick}
      className={cn(
        "w-full bg-surface-panel hover:bg-interactive-hover transition-all rounded-lg px-4 py-2.5",
        "flex items-center justify-between group",
        "border border-border-divider",
        className
      )}
    >
      <div className="flex items-center space-x-3">
        <GitBranch className="h-4 w-4 text-text-secondary" />
        <span className="text-sm font-medium text-text-secondary">
          {diffSummary.filesChanged} {diffSummary.filesChanged === 1 ? 'file' : 'files'} changed
        </span>
        <div className="flex items-center space-x-2 text-xs">
          <span className="text-functional-positive">+{diffSummary.totalAdditions}</span>
          <span className="text-functional-negative">-{diffSummary.totalDeletions}</span>
        </div>
      </div>
      
      <span className="text-xs text-text-secondary group-hover:text-text-primary transition-colors">
        Click to review
      </span>
    </button>
  )
}