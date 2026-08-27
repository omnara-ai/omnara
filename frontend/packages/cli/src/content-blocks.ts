interface ContentBlockLike {
  type: string
  text?: string
  name?: string
  value?: unknown
}

export function blockText(block: ContentBlockLike): string {
  switch (block.type) {
    case 'text':
      return block.text ?? ''
    case 'tool_call':
      return `[tool_call ${block.name ?? ''}]`
    case 'structured_data':
      return JSON.stringify(block.value)
    default:
      return `[${block.type}]`
  }
}
