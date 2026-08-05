import type { CreateAgentInputContentBlock } from '@omnara/sdk'

const skillRefPattern = /\$\[([^\][]+)\]/g

function extractSkillRefs(text: string): { text: string; skills: string[] } {
  const skills: string[] = []
  const stripped = text.replace(skillRefPattern, (_, name: string) => {
    const skill = name.trim()
    if (skill !== '' && !skills.includes(skill)) skills.push(skill)
    return `the "${skill}" skill`
  })
  return { text: stripped, skills }
}

export function promptContentBlocks(input: string): CreateAgentInputContentBlock[] {
  const { text, skills } = extractSkillRefs(input)
  const blocks: CreateAgentInputContentBlock[] = []
  const trimmed = text.trim()
  if (trimmed !== '') blocks.push({ type: 'text', text: trimmed })
  if (skills.length > 0) {
    const names = skills.map((skill) => `"${skill}"`).join(', ')
    blocks.push({
      type: 'text',
      text: `Use the ${names} skill${skills.length > 1 ? 's' : ''} to handle this request.`,
    })
  }
  if (blocks.length === 0) throw new Error('prompt must include text')
  return blocks
}
