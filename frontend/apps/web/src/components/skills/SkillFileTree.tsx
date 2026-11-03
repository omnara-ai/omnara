import type { SkillFile } from '@omnara/sdk'
import { useMemo, useState } from 'react'

import { ChevronRight, File, Folder } from '@/components/icons'
import { skillFileTree, type SkillFileTreeNode } from '@/lib/skill-file-tree'
import { cn } from '@/lib/utils'

/** A skill's archive files as a scrollable folder tree; size it with `className`. */
export function SkillFileTree({ files, className }: { files: SkillFile[]; className?: string }) {
  const tree = useMemo(() => skillFileTree(files.map((file) => file.path)), [files])
  return (
    <ul
      aria-label="Skill files"
      className={cn('min-w-0 overflow-y-auto font-mono text-xs leading-6', className)}
    >
      {tree.map((node) => (
        <SkillFileTreeItem key={node.path} node={node} />
      ))}
    </ul>
  )
}

function SkillFileTreeItem({ node }: { node: SkillFileTreeNode }) {
  const [open, setOpen] = useState(true)
  if (!node.children) {
    return (
      <li className="flex min-w-0 items-center gap-1.5 pl-5" title={node.path}>
        <File aria-hidden="true" className="text-muted-foreground size-3.5 shrink-0" />
        <span className="truncate">{node.name}</span>
      </li>
    )
  }
  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        title={node.path}
        className="hover:bg-muted focus-visible:ring-ring/50 flex w-full min-w-0 items-center gap-1.5 rounded-sm text-left outline-none focus-visible:ring-2"
        onClick={() => {
          setOpen((current) => !current)
        }}
      >
        <ChevronRight
          aria-hidden="true"
          className={cn('size-3.5 shrink-0 transition-transform', open && 'rotate-90')}
        />
        <Folder aria-hidden="true" className="text-muted-foreground size-3.5 shrink-0" />
        <span className="truncate">{node.name}</span>
      </button>
      {open && (
        <ul className="ml-[0.4375rem] border-l pl-2">
          {node.children.map((child) => (
            <SkillFileTreeItem key={child.path} node={child} />
          ))}
        </ul>
      )}
    </li>
  )
}
