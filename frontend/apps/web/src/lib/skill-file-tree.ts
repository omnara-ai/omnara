export interface SkillFileTreeNode {
  name: string
  path: string
  /** Set for folders; files have no children. */
  children?: SkillFileTreeNode[]
}

/** Nests slash-separated archive paths into folders, folders before files, each sorted by name. */
export function skillFileTree(paths: string[]): SkillFileTreeNode[] {
  const root: SkillFileTreeNode[] = []
  for (const path of paths) {
    const parts = path.split('/').filter(Boolean)
    let level = root
    for (const [index, name] of parts.entries()) {
      const isFile = index === parts.length - 1
      let node = level.find((entry) => entry.name === name && !entry.children === isFile)
      if (!node) {
        const nodePath = parts.slice(0, index + 1).join('/')
        node = isFile ? { name, path: nodePath } : { name, path: nodePath, children: [] }
        level.push(node)
      }
      if (node.children) level = node.children
    }
  }
  return sortTree(root)
}

function sortTree(nodes: SkillFileTreeNode[]): SkillFileTreeNode[] {
  return nodes
    .map((node) => (node.children ? { ...node, children: sortTree(node.children) } : node))
    .sort(
      (left, right) =>
        Number(!left.children) - Number(!right.children) || left.name.localeCompare(right.name),
    )
}
