export interface DiffFile {
  filename: string
  additions: number
  deletions: number
  content: string
}

export interface DiffSummary {
  files: DiffFile[]
  totalAdditions: number
  totalDeletions: number
  filesChanged: number
}

export interface TreeNode {
  name: string
  path: string
  type: 'file' | 'directory'
  children?: TreeNode[]
  additions?: number
  deletions?: number
  expanded?: boolean
  fileCount?: number
}

export function parseGitDiff(diff: string | null | undefined): DiffSummary | null {
  if (!diff || diff.trim() === '') {
    return null
  }

  const files: DiffFile[] = []
  let currentFile: DiffFile | null = null
  let currentFileContent: string[] = []
  let totalAdditions = 0
  let totalDeletions = 0

  const lines = diff.split('\n')
  
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    
    // Match file header: diff --git a/path b/path
    if (line.startsWith('diff --git')) {
      // Save previous file if exists
      if (currentFile) {
        currentFile.content = currentFileContent.join('\n')
        files.push(currentFile)
        currentFileContent = []
      }
      
      // Extract filename from the diff header
      const match = line.match(/diff --git a\/(.*?) b\//)
      const filename = match ? match[1] : 'unknown'
      
      currentFile = {
        filename,
        additions: 0,
        deletions: 0,
        content: ''
      }
    }
    
    // Collect all lines for current file
    if (currentFile) {
      currentFileContent.push(line)
      
      // Track additions
      if (line.startsWith('+') && !line.startsWith('+++')) {
        currentFile.additions++
        totalAdditions++
      }
      // Track deletions
      else if (line.startsWith('-') && !line.startsWith('---')) {
        currentFile.deletions++
        totalDeletions++
      }
    }
  }

  // Don't forget the last file
  if (currentFile) {
    currentFile.content = currentFileContent.join('\n')
    files.push(currentFile)
  }

  return {
    files,
    totalAdditions,
    totalDeletions,
    filesChanged: files.length
  }
}

export function formatDiffSummary(summary: DiffSummary): string {
  const { filesChanged, totalAdditions, totalDeletions } = summary
  const fileText = filesChanged === 1 ? 'file' : 'files'
  return `${filesChanged} ${fileText} changed, +${totalAdditions} lines, -${totalDeletions} lines`
}

export function buildFileTree(files: DiffFile[]): TreeNode[] {
  const root: TreeNode[] = []
  
  files.forEach(file => {
    const parts = file.filename.split('/')
    let currentLevel = root
    let currentPath = ''
    
    parts.forEach((part, index) => {
      currentPath = currentPath ? `${currentPath}/${part}` : part
      const isLastPart = index === parts.length - 1
      
      if (isLastPart) {
        // It's a file
        currentLevel.push({
          name: part,
          path: file.filename,
          type: 'file',
          additions: file.additions,
          deletions: file.deletions
        })
      } else {
        // It's a directory
        let existingDir = currentLevel.find(node => node.name === part && node.type === 'directory')
        
        if (!existingDir) {
          existingDir = {
            name: part,
            path: currentPath,
            type: 'directory',
            children: [],
            expanded: true // Default to expanded
          }
          currentLevel.push(existingDir)
        }
        
        currentLevel = existingDir.children!
      }
    })
  })
  
  // Calculate file counts and sort
  const processNodes = (nodes: TreeNode[]) => {
    nodes.forEach(node => {
      if (node.type === 'directory' && node.children) {
        processNodes(node.children)
        
        // Calculate total file count for this directory
        let fileCount = 0
        const countFiles = (children: TreeNode[]) => {
          children.forEach(child => {
            if (child.type === 'file') {
              fileCount++
            } else if (child.children) {
              countFiles(child.children)
            }
          })
        }
        countFiles(node.children)
        node.fileCount = fileCount
      }
    })
    
    // Sort directories first, then files, alphabetically
    nodes.sort((a, b) => {
      if (a.type === 'directory' && b.type === 'file') return -1
      if (a.type === 'file' && b.type === 'directory') return 1
      return a.name.localeCompare(b.name)
    })
  }
  
  processNodes(root)
  return root
}