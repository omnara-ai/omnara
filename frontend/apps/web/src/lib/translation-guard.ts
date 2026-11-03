export function installTranslationGuard(target: { Node: typeof Node }): () => void {
  const { prototype } = target.Node
  // eslint-disable-next-line @typescript-eslint/unbound-method -- called below with the parent node as this
  const { removeChild, insertBefore } = prototype

  prototype.removeChild = function removeAttachedChild<T extends Node>(this: Node, child: T): T {
    if (child.parentNode === this) removeChild.call(this, child)
    return child
  }
  prototype.insertBefore = function insertBeforeAttachedChild<T extends Node>(
    this: Node,
    node: T,
    child: Node | null,
  ): T {
    insertBefore.call(this, node, child?.parentNode === this ? child : null)
    return node
  }

  return () => {
    prototype.removeChild = removeChild
    prototype.insertBefore = insertBefore
  }
}
