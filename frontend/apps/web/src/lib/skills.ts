import type { ProjectSkillAccess, Skill } from '@omnara/sdk'

export function skillOwnerLabel(skill: Skill) {
  if (skill.owner.kind === 'org') return 'Organization'
  if (skill.owner.kind === 'project') return 'Project'
  return 'User'
}

export function projectSkillOwnerLabel(access: ProjectSkillAccess) {
  return skillOwnerLabel(access.skill)
}
