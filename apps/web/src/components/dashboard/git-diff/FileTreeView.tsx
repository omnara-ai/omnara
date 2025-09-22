import { useState, useCallback, useMemo } from 'react'
import { ChevronRight, ChevronDown, Folder, FolderOpen, FileCode2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { type TreeNode } from '@/utils/gitDiffParser'

interface FileTreeViewProps {
  nodes: TreeNode[]
  selectedPath: string | null
  onSelectFile: (path: string) => void
  searchQuery?: string
  className?: string
}

interface TreeItemProps {
  node: TreeNode
  level: number
  selectedPath: string | null
  onSelectFile: (path: string) => void
  expandedPaths: Set<string>
  onToggleExpand: (path: string) => void
  searchQuery?: string
}

function TreeItem({ 
  node, 
  level, 
  selectedPath, 
  onSelectFile, 
  expandedPaths,
  onToggleExpand,
  searchQuery 
}: TreeItemProps) {
  const isExpanded = expandedPaths.has(node.path)
  const isSelected = selectedPath === node.path
  const hasMatch = searchQuery ? node.name.toLowerCase().includes(searchQuery.toLowerCase()) : true
  
  const handleClick = useCallback(() => {
    if (node.type === 'directory') {
      onToggleExpand(node.path)
    } else {
      onSelectFile(node.path)
    }
  }, [node, onToggleExpand, onSelectFile])
  
  const handleChevronClick = useCallback((e: React.MouseEvent) => {
    e.stopPropagation()
    if (node.type === 'directory') {
      onToggleExpand(node.path)
    }
  }, [node, onToggleExpand])
  
  // If searching and no match, check children
  const hasChildMatch = useMemo(() => {
    if (!searchQuery || hasMatch) return true
    if (node.type === 'file') return hasMatch
    
    const checkChildren = (children: TreeNode[]): boolean => {
      return children.some(child => {
        if (child.name.toLowerCase().includes(searchQuery.toLowerCase())) return true
        if (child.type === 'directory' && child.children) {
          return checkChildren(child.children)
        }
        return false
      })
    }
    
    return node.children ? checkChildren(node.children) : false
  }, [node, searchQuery, hasMatch])
  
  if (searchQuery && !hasMatch && !hasChildMatch) {
    return null
  }
  
  return (
    <div>
      <button
        onClick={handleClick}
        className={cn(
          "w-full flex items-center space-x-1 pl-2 pr-4 py-1.5 text-left rounded-md transition-all",
          "hover:bg-white/5",
          isSelected && node.type === 'file' && "bg-electric-accent/20 text-electric-accent",
          !isSelected && "text-off-white/70 hover:text-off-white"
        )}
        style={{ paddingLeft: `${level * 12 + 8}px` }}
      >
        {node.type === 'directory' && (
          <button
            onClick={handleChevronClick}
            className="p-0.5 hover:bg-white/10 rounded transition-colors"
          >
            {isExpanded ? (
              <ChevronDown className="h-3.5 w-3.5 transition-transform duration-200" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 transition-transform duration-200" />
            )}
          </button>
        )}
        
        {node.type === 'directory' ? (
          isExpanded ? (
            <FolderOpen className="h-4 w-4 text-electric-accent/60" />
          ) : (
            <Folder className="h-4 w-4 text-electric-accent/60" />
          )
        ) : (
          <FileCode2 className="h-4 w-4 text-off-white/60 ml-5" />
        )}
        
        <span className={cn(
          "text-sm font-mono truncate flex-1",
          hasMatch && searchQuery && "text-electric-accent"
        )}>
          {node.name}
        </span>
        
        {node.type === 'directory' && node.fileCount !== undefined && (
          <span className="text-xs text-off-white/40 ml-2">
            {node.fileCount}
          </span>
        )}
        
        {node.type === 'file' && (
          <div className="flex items-center space-x-1.5 text-xs ml-2">
            <span className="text-green-400">+{node.additions}</span>
            <span className="text-red-400">-{node.deletions}</span>
          </div>
        )}
      </button>
      
      {node.type === 'directory' && isExpanded && node.children && (
        <div>
          {node.children.map((child) => (
            <TreeItem
              key={child.path}
              node={child}
              level={level + 1}
              selectedPath={selectedPath}
              onSelectFile={onSelectFile}
              expandedPaths={expandedPaths}
              onToggleExpand={onToggleExpand}
              searchQuery={searchQuery}
            />
          ))}
        </div>
      )}
    </div>
  )
}

export function FileTreeView({ 
  nodes, 
  selectedPath, 
  onSelectFile, 
  searchQuery,
  className 
}: FileTreeViewProps) {
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(() => {
    // Initialize with all directories expanded
    const expanded = new Set<string>()
    const addAllPaths = (nodes: TreeNode[]) => {
      nodes.forEach(node => {
        if (node.type === 'directory') {
          expanded.add(node.path)
          if (node.children) {
            addAllPaths(node.children)
          }
        }
      })
    }
    addAllPaths(nodes)
    return expanded
  })
  
  const handleToggleExpand = useCallback((path: string) => {
    setExpandedPaths(prev => {
      const newSet = new Set(prev)
      if (newSet.has(path)) {
        newSet.delete(path)
      } else {
        newSet.add(path)
      }
      return newSet
    })
  }, [])
  
  const handleExpandAll = useCallback(() => {
    const allPaths = new Set<string>()
    const collectPaths = (nodes: TreeNode[]) => {
      nodes.forEach(node => {
        if (node.type === 'directory') {
          allPaths.add(node.path)
          if (node.children) {
            collectPaths(node.children)
          }
        }
      })
    }
    collectPaths(nodes)
    setExpandedPaths(allPaths)
  }, [nodes])
  
  const handleCollapseAll = useCallback(() => {
    setExpandedPaths(new Set())
  }, [])
  
  return (
    <div className={cn("flex flex-col h-full", className)}>
      {/* Expand/Collapse All buttons */}
      <div className="px-3 py-2 border-b border-white/10 flex items-center justify-between">
        <span className="text-xs text-off-white/60 font-medium">Files</span>
        <div className="flex items-center space-x-1">
          <button
            onClick={handleExpandAll}
            className="p-1 hover:bg-white/10 rounded text-off-white/60 hover:text-off-white transition-colors"
            title="Expand all"
          >
            <ChevronDown className="h-3.5 w-3.5" />
          </button>
          <button
            onClick={handleCollapseAll}
            className="p-1 hover:bg-white/10 rounded text-off-white/60 hover:text-off-white transition-colors"
            title="Collapse all"
          >
            <ChevronRight className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      
      {/* Tree */}
      <div 
        className="flex-1 overflow-y-auto py-2 min-h-0 [&::-webkit-scrollbar]:w-[6px] [&::-webkit-scrollbar-track]:bg-transparent [&::-webkit-scrollbar-thumb]:bg-transparent [&::-webkit-scrollbar-thumb]:rounded-[3px] [&::-webkit-scrollbar-thumb:hover]:bg-transparent"
        style={{
          scrollbarWidth: 'thin',
          scrollbarColor: 'transparent transparent'
        }}
      >
        {nodes.map(node => (
          <TreeItem
            key={node.path}
            node={node}
            level={0}
            selectedPath={selectedPath}
            onSelectFile={onSelectFile}
            expandedPaths={expandedPaths}
            onToggleExpand={handleToggleExpand}
            searchQuery={searchQuery}
          />
        ))}
      </div>
    </div>
  )
}