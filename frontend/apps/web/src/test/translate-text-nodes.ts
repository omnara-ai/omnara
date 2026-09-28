export function translateTextNodes(root: Node) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const textNodes: Node[] = []
  while (walker.nextNode()) textNodes.push(walker.currentNode)
  for (const text of textNodes) {
    const translated = document.createElement('span')
    translated.textContent = text.textContent
    text.parentNode?.replaceChild(translated, text)
  }
}
