import { useState, useEffect } from 'react'
import { GitDiffStatusBar } from './GitDiffStatusBar'
import { GitDiffReviewPanel } from './GitDiffReviewPanel'
import { AnimatePresence } from 'framer-motion'
import { cn } from '@/lib/utils'

type ViewMode = 'collapsed' | 'review' | 'fullscreen'

interface GitDiffViewProps {
  gitDiff: string | null | undefined
  hasInputBelow?: boolean
  className?: string
}

const VIEW_STATE_KEY = 'git-diff-view-state'

export function GitDiffView({ gitDiff, hasInputBelow = true, className }: GitDiffViewProps) {
  const [viewMode, setViewMode] = useState<ViewMode>('collapsed')

  // Remember the last state for this session
  useEffect(() => {
    const savedState = sessionStorage.getItem(VIEW_STATE_KEY)
    if (savedState === 'review') {
      setViewMode('review')
    }
  }, [])

  useEffect(() => {
    sessionStorage.setItem(VIEW_STATE_KEY, viewMode)
  }, [viewMode])

  const handleStatusBarClick = () => {
    setViewMode('review')
  }

  const handleClose = () => {
    setViewMode('collapsed')
  }

  const handleFullscreen = () => {
    setViewMode('fullscreen')
  }

  const handleExitFullscreen = () => {
    setViewMode('review')
  }

  // Fullscreen mode
  if (viewMode === 'fullscreen') {
    return (
      <div className="fixed inset-0 z-50 bg-charcoal">
        <GitDiffReviewPanel
          gitDiff={gitDiff}
          onClose={handleExitFullscreen}
          height="100vh"
        />
      </div>
    )
  }

  return (
    <div className={cn("w-full", className)}>
      <AnimatePresence mode="wait">
        {viewMode === 'collapsed' ? (
          <GitDiffStatusBar
            key="status-bar"
            gitDiff={gitDiff}
            onClick={handleStatusBarClick}
          />
        ) : (
          <GitDiffReviewPanel
            key="review-panel"
            gitDiff={gitDiff}
            onClose={handleClose}
            onFullscreen={handleFullscreen}
          />
        )}
      </AnimatePresence>
    </div>
  )
}