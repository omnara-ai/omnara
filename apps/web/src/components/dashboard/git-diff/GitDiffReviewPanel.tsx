import { useState, useMemo, useCallback } from 'react'
import { X, Maximize2, FileCode2, ChevronLeft, ChevronRight, Search, PanelLeft, PanelLeftClose } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { ResizablePanelGroup, ResizablePanel, ResizableHandle } from '@/components/ui/resizable'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { parseGitDiff, buildFileTree, type DiffFile } from '@/utils/gitDiffParser'
import { FileTreeView } from './FileTreeView'
import { motion } from 'framer-motion'
import { cn } from '@/lib/utils'

interface GitDiffReviewPanelProps {
  gitDiff: string | null | undefined
  onClose: () => void
  onFullscreen?: () => void
  height?: string
}

interface DiffLine {
  type: 'header' | 'file' | 'hunk' | 'addition' | 'deletion' | 'context'
  content: string
  oldLineNumber?: number
  newLineNumber?: number
}

function parseDiffLines(diff: string): DiffLine[] {
  const lines = diff.split('\n')
  const parsedLines: DiffLine[] = []
  let oldLineNum = 0
  let newLineNum = 0
  
  lines.forEach((line) => {
    if (line.startsWith('diff --git')) {
      parsedLines.push({ type: 'header', content: line })
    } else if (line.startsWith('index ') || line.startsWith('new file mode') || line.startsWith('deleted file mode')) {
      parsedLines.push({ type: 'file', content: line })
    } else if (line.startsWith('---') || line.startsWith('+++')) {
      parsedLines.push({ type: 'file', content: line })
    } else if (line.startsWith('@@')) {
      const match = line.match(/@@ -(\d+),?\d* \+(\d+),?\d* @@/)
      if (match) {
        oldLineNum = parseInt(match[1])
        newLineNum = parseInt(match[2])
      }
      parsedLines.push({ type: 'hunk', content: line })
    } else if (line.startsWith('+')) {
      parsedLines.push({ 
        type: 'addition', 
        content: line,
        newLineNumber: newLineNum++
      })
    } else if (line.startsWith('-')) {
      parsedLines.push({ 
        type: 'deletion', 
        content: line,
        oldLineNumber: oldLineNum++
      })
    } else {
      parsedLines.push({ 
        type: 'context', 
        content: line,
        oldLineNumber: oldLineNum++,
        newLineNumber: newLineNum++
      })
    }
  })
  
  return parsedLines
}

export function GitDiffReviewPanel({ 
  gitDiff, 
  onClose, 
  onFullscreen,
  height = '70vh'
}: GitDiffReviewPanelProps) {
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [showTreeView, setShowTreeView] = useState(false)
  
  const diffSummary = useMemo(() => parseGitDiff(gitDiff), [gitDiff])
  
  const fileTree = useMemo(() => {
    if (!diffSummary) return []
    return buildFileTree(diffSummary.files)
  }, [diffSummary])
  
  // Auto-select first file
  useMemo(() => {
    if (diffSummary && diffSummary.files.length > 0 && !selectedFile) {
      setSelectedFile(diffSummary.files[0].filename)
    }
  }, [diffSummary, selectedFile])

  const selectedFileData = useMemo(() => {
    if (!selectedFile || !diffSummary) return null
    return diffSummary.files.find(f => f.filename === selectedFile)
  }, [selectedFile, diffSummary])

  const handleNavigate = useCallback((direction: 'prev' | 'next') => {
    if (!diffSummary || !selectedFile) return
    
    const currentIndex = diffSummary.files.findIndex(f => f.filename === selectedFile)
    if (currentIndex === -1) return
    
    const newIndex = direction === 'prev' ? currentIndex - 1 : currentIndex + 1
    if (newIndex >= 0 && newIndex < diffSummary.files.length) {
      setSelectedFile(diffSummary.files[newIndex].filename)
    }
  }, [diffSummary, selectedFile])

  const canNavigate = useMemo(() => {
    if (!diffSummary || !selectedFile) return { prev: false, next: false }
    
    const currentIndex = diffSummary.files.findIndex(f => f.filename === selectedFile)
    return {
      prev: currentIndex > 0,
      next: currentIndex < diffSummary.files.length - 1
    }
  }, [diffSummary, selectedFile])
  
  const currentFileIndex = useMemo(() => {
    if (!diffSummary || !selectedFile) return 0
    return diffSummary.files.findIndex(f => f.filename === selectedFile)
  }, [diffSummary, selectedFile])

  if (!diffSummary || diffSummary.files.length === 0) {
    return null
  }

  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: 10 }}
      transition={{ duration: 0.2 }}
      className="bg-white/5 backdrop-blur-sm rounded-xl overflow-hidden border border-white/10 flex flex-col"
      style={{ height }}
    >
      {!showTreeView ? (
        // Focus Mode - Full width single file view
        <>
          {/* Header with navigation */}
          <div className="header-translucent">
            <div className="flex items-center justify-between">
              <div className="flex items-center space-x-3">
                {/* Previous button */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleNavigate('prev')}
                  disabled={!canNavigate.prev}
                  className="h-8 px-3 text-off-white hover:text-off-white hover:bg-white/10 disabled:opacity-50 disabled:hover:bg-transparent"
                >
                  <ChevronLeft className="h-4 w-4 mr-1" />
                  Previous
                </Button>
                
                {/* File selector */}
                <Select value={selectedFile || ''} onValueChange={setSelectedFile}>
                  <SelectTrigger className="h-8 w-auto max-w-md bg-black/30 border-white/20 text-off-white text-sm hover:bg-white/10 transition-colors">
                    <SelectValue>
                      {selectedFile && (
                        <span className="flex items-center">
                          <span className="text-off-white/60 hidden sm:inline-block mr-2">
                            File {currentFileIndex + 1} of {diffSummary.files.length}:
                          </span>
                          <span className="font-medium truncate">{selectedFile.split('/').pop()}</span>
                        </span>
                      )}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent className="bg-charcoal border-white/20 max-h-[500px]">
                    {diffSummary.files.map((file, index) => (
                      <SelectItem 
                        key={file.filename} 
                        value={file.filename} 
                        className="text-off-white hover:text-off-white hover:bg-white/10 focus:text-off-white focus:bg-white/10 cursor-pointer data-[highlighted]:text-off-white data-[highlighted]:bg-white/10"
                      >
                        <div className="flex items-center justify-between w-full">
                          <span className="font-mono text-sm">
                            <span className="text-off-white/60 mr-2">{index + 1}.</span>
                            {file.filename}
                          </span>
                          <div className="flex items-center space-x-2 text-xs ml-4">
                            <span className="text-green-400">+{file.additions}</span>
                            <span className="text-red-400">-{file.deletions}</span>
                          </div>
                        </div>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                
                {/* Next button */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleNavigate('next')}
                  disabled={!canNavigate.next}
                  className="h-8 px-3 text-off-white hover:text-off-white hover:bg-white/10 disabled:opacity-50 disabled:hover:bg-transparent"
                >
                  Next
                  <ChevronRight className="h-4 w-4 ml-1" />
                </Button>
              </div>
              
              <div className="flex items-center space-x-2">
                {/* Show tree view button */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setShowTreeView(true)}
                  className="h-8 px-3 text-off-white hover:text-off-white hover:bg-white/10"
                >
                  <PanelLeft className="h-4 w-4 mr-2" />
                  Show Tree View
                </Button>
                
                {onFullscreen && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={onFullscreen}
                    className="h-8 w-8 p-0 text-off-white hover:text-off-white hover:bg-white/10"
                    title="Fullscreen"
                  >
                    <Maximize2 className="h-4 w-4" />
                  </Button>
                )}
                
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={onClose}
                  className="h-8 w-8 p-0 text-off-white hover:text-off-white hover:bg-white/10"
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            </div>
          </div>
          
          {/* Full width diff content */}
          {selectedFileData ? (
            <ScrollArea className="h-[calc(100%-3.5rem)] scroll-shadow-y [&_[data-radix-scroll-area-viewport]>div]:!block">
              <div className="p-6 overflow-x-auto scrollbar-thin scrollbar-transparent">
                <div className="font-mono text-sm min-w-fit">
                  {parseDiffLines(selectedFileData.content).map((line, index) => (
                    <div
                      key={index}
                      className={cn(
                        "flex whitespace-pre",
                        line.type === 'header' && "text-electric-accent font-bold mb-2",
                        line.type === 'file' && "text-off-white/70 text-xs mb-1",
                        line.type === 'hunk' && "text-cyan-400 bg-midnight-blue/50 px-2 py-1 my-2 rounded text-xs",
                        line.type === 'addition' && "bg-green-500/10 text-green-400",
                        line.type === 'deletion' && "bg-red-500/10 text-red-400",
                        line.type === 'context' && "text-off-white/50"
                      )}
                    >
                      {line.type !== 'header' && line.type !== 'file' && line.type !== 'hunk' && (
                        <>
                          <span className="inline-block w-12 text-right pr-3 text-off-white/30 text-xs select-none">
                            {line.oldLineNumber || ''}
                          </span>
                          <span className="inline-block w-12 text-right pr-3 text-off-white/30 text-xs select-none">
                            {line.newLineNumber || ''}
                          </span>
                          <span className={cn(
                            "inline-block w-6 text-center select-none",
                            line.type === 'addition' ? 'text-green-400' : 
                            line.type === 'deletion' ? 'text-red-400' : 
                            'text-off-white/30'
                          )}>
                            {line.type === 'addition' ? '+' : line.type === 'deletion' ? '-' : ' '}
                          </span>
                        </>
                      )}
                      <span className="pr-4">
                        {line.type === 'header' || line.type === 'file' || line.type === 'hunk' 
                          ? line.content 
                          : line.content.substring(1) || ' '}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            </ScrollArea>
          ) : (
            <div className="h-[calc(100%-3.5rem)] flex items-center justify-center text-off-white/40 text-sm">
              Select a file to view changes
            </div>
          )}
        </>
      ) : (
        // Tree View Mode - Current two panel layout
        <>
          {/* Header */}
          <div className="px-4 py-3 bg-black/50 backdrop-blur-sm border-b border-white/10 flex items-center justify-between">
            <div className="flex items-center space-x-3">
              <span className="text-sm font-semibold text-off-white">Code Review</span>
              <span className="text-xs text-off-white/60">
                {diffSummary.filesChanged} files | 
                <span className="text-green-400 mx-1">+{diffSummary.totalAdditions}</span>
                <span className="text-red-400">-{diffSummary.totalDeletions}</span>
              </span>
            </div>
            
            <div className="flex items-center space-x-2">
              {/* Hide tree view button */}
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setShowTreeView(false)}
                className="h-8 px-3 text-off-white hover:text-off-white hover:bg-white/10"
              >
                <PanelLeftClose className="h-4 w-4 mr-2" />
                Hide Tree View
              </Button>
              
              {onFullscreen && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={onFullscreen}
                  className="h-8 w-8 p-0 text-off-white hover:text-off-white hover:bg-white/10"
                  title="Fullscreen"
                >
                  <Maximize2 className="h-4 w-4" />
                </Button>
              )}
              <Button
                variant="ghost"
                size="sm"
                onClick={onClose}
                className="h-8 w-8 p-0 text-off-white hover:text-off-white hover:bg-white/10"
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          </div>

      {/* Content */}
      <ResizablePanelGroup direction="horizontal" className="flex-1">
        {/* File List */}
        <ResizablePanel defaultSize={25} minSize={20} maxSize={40}>
          <div className="h-full flex flex-col bg-black/20 overflow-hidden">
            {/* Search */}
            <div className="p-3 border-b border-white/10">
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-4 w-4 text-off-white/40" />
                <Input
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  placeholder="Search files..."
                  className="pl-8 h-8 bg-white/5 border-white/10 text-off-white placeholder:text-off-white/40 text-sm"
                />
              </div>
            </div>
            
            {/* File Tree */}
            <FileTreeView
              nodes={fileTree}
              selectedPath={selectedFile}
              onSelectFile={setSelectedFile}
              searchQuery={searchQuery}
              className="flex-1 min-h-0"
            />
          </div>
        </ResizablePanel>

        <ResizableHandle withHandle />

        {/* Diff Viewer */}
        <ResizablePanel defaultSize={75}>
          {selectedFileData ? (
            <div className="h-full flex flex-col">
              {/* File Header */}
              <div className="header-translucent px-4 py-2 flex items-center justify-between sticky top-0 z-10">
                <div className="flex items-center space-x-2 min-w-0">
                  <FileCode2 className="h-4 w-4 text-electric-accent flex-shrink-0" />
                  <span className="font-mono text-sm text-off-white truncate">
                    {selectedFileData.filename}
                  </span>
                </div>
                
                {/* Navigation */}
                <div className="flex items-center space-x-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleNavigate('prev')}
                    disabled={!canNavigate.prev}
                    className="h-7 w-7 p-0"
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleNavigate('next')}
                    disabled={!canNavigate.next}
                    className="h-7 w-7 p-0"
                  >
                    <ChevronRight className="h-4 w-4" />
                  </Button>
                </div>
              </div>

              {/* Diff Content */}
              <ScrollArea className="flex-1 scroll-shadow-y [&_[data-radix-scroll-area-viewport]>div]:!block">
                <div className="p-4 overflow-x-auto scrollbar-thin scrollbar-transparent">
                  <div className="font-mono text-sm min-w-fit">
                    {parseDiffLines(selectedFileData.content).map((line, index) => (
                      <div
                        key={index}
                        className={cn(
                          "flex whitespace-pre",
                          line.type === 'header' && "text-electric-accent font-bold mb-2",
                          line.type === 'file' && "text-off-white/70 text-xs mb-1",
                          line.type === 'hunk' && "text-cyan-400 bg-midnight-blue/50 px-2 py-1 my-2 rounded text-xs",
                          line.type === 'addition' && "bg-green-500/10 text-green-400",
                          line.type === 'deletion' && "bg-red-500/10 text-red-400",
                          line.type === 'context' && "text-off-white/50"
                        )}
                      >
                        {line.type !== 'header' && line.type !== 'file' && line.type !== 'hunk' && (
                          <>
                            <span className="inline-block w-12 text-right pr-3 text-off-white/30 text-xs select-none">
                              {line.oldLineNumber || ''}
                            </span>
                            <span className="inline-block w-12 text-right pr-3 text-off-white/30 text-xs select-none">
                              {line.newLineNumber || ''}
                            </span>
                            <span className={cn(
                              "inline-block w-6 text-center select-none",
                              line.type === 'addition' ? 'text-green-400' : 
                              line.type === 'deletion' ? 'text-red-400' : 
                              'text-off-white/30'
                            )}>
                              {line.type === 'addition' ? '+' : line.type === 'deletion' ? '-' : ' '}
                            </span>
                          </>
                        )}
                        <span className="pr-4">
                          {line.type === 'header' || line.type === 'file' || line.type === 'hunk' 
                            ? line.content 
                            : line.content.substring(1) || ' '}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              </ScrollArea>
            </div>
          ) : (
            <div className="h-full flex items-center justify-center text-off-white/40 text-sm">
              Select a file to view changes
            </div>
          )}
        </ResizablePanel>
      </ResizablePanelGroup>
        </>
      )}
    </motion.div>
  )
}
