import { readFile } from 'node:fs/promises'
import path from 'node:path'

import type { CreateAgentInputContentBlock, InlineMediaContentBlock } from '@omnara/sdk'

type MediaType = InlineMediaContentBlock['media_type']

const mediaTypesByExtension: Record<string, MediaType> = {
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.pdf': 'application/pdf',
  '.txt': 'text/plain',
  '.yaml': 'text/plain',
  '.yml': 'text/plain',
  '.md': 'text/markdown',
  '.markdown': 'text/markdown',
  '.csv': 'text/csv',
  '.tsv': 'text/tab-separated-values',
  '.docx': 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  '.pptx': 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  '.xlsx': 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
}

interface AttachmentRef {
  start: number
  end: number
  filename: string
}

function isPromptSpace(ch: string): boolean {
  return ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r'
}

function parseAttachmentRefs(text: string): AttachmentRef[] {
  const refs: AttachmentRef[] = []
  let cursor = 0
  while (cursor < text.length) {
    const start = text.indexOf('@', cursor)
    if (start < 0) break
    if (start > 0 && !isPromptSpace(text[start - 1]!)) {
      cursor = start + 1
      continue
    }
    if (start + 1 >= text.length) break
    if (text[start + 1] === '[') {
      const nameStart = start + 2
      const nameEnd = text.indexOf(']', nameStart)
      if (nameEnd < 0) {
        throw new Error(`unterminated attachment reference starting at ${JSON.stringify(text.slice(start))}`)
      }
      const filename = text.slice(nameStart, nameEnd).trim()
      if (filename === '') throw new Error('attachment filename must not be empty')
      refs.push({ start, end: nameEnd + 1, filename })
      cursor = nameEnd + 1
      continue
    }
    let end = start + 1
    while (end < text.length && !isPromptSpace(text[end]!)) end++
    const filename = text.slice(start + 1, end).trim()
    if (filename === '') {
      cursor = start + 1
      continue
    }
    refs.push({ start, end, filename })
    cursor = end
  }
  return refs
}

async function attachmentBlock(filename: string): Promise<CreateAgentInputContentBlock> {
  const data = await readFile(filename)
  if (data.length === 0) throw new Error(`attachment ${filename} must not be empty`)
  let mediaType = mediaTypesByExtension[path.extname(filename).toLowerCase()]
  if (mediaType == null && !data.includes(0)) mediaType = 'text/plain'
  if (mediaType == null) {
    throw new Error(`attachment ${filename} has an unsupported file type`)
  }
  return {
    type: 'media',
    media_type: mediaType,
    filename: path.basename(filename),
    data: data.toString('base64'),
  }
}

const skillRefPattern = /\$\[([^\][]+)\]/g

export function extractSkillRefs(text: string): { text: string; skills: string[] } {
  const skills: string[] = []
  const stripped = text.replace(skillRefPattern, (_, name: string) => {
    const skill = name.trim()
    if (skill !== '' && !skills.includes(skill)) skills.push(skill)
    return `the "${skill}" skill`
  })
  return { text: stripped, skills }
}

export async function promptContentBlocks(input: string): Promise<CreateAgentInputContentBlock[]> {
  const { text, skills } = extractSkillRefs(input)
  const refs = parseAttachmentRefs(text)
  const blocks: CreateAgentInputContentBlock[] = []
  let cursor = 0
  for (const ref of refs) {
    const before = text.slice(cursor, ref.start).trim()
    if (before !== '') blocks.push({ type: 'text', text: before })
    blocks.push(await attachmentBlock(ref.filename))
    cursor = ref.end
  }
  const after = text.slice(cursor).trim()
  if (after !== '') blocks.push({ type: 'text', text: after })
  if (skills.length > 0) {
    const names = skills.map((skill) => `"${skill}"`).join(', ')
    blocks.push({
      type: 'text',
      text: `Use the ${names} skill${skills.length > 1 ? 's' : ''} to handle this request.`,
    })
  }
  if (blocks.length === 0) throw new Error('prompt must include text or at least one attachment')
  return blocks
}
