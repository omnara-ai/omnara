import { useSkill } from '@omnara/react'
import type { Skill } from '@omnara/sdk'

import { DetailList } from '@/components/data-table/DetailList'
import { formatDateTime } from '@/lib/format'

export function SkillDetails({ orgId, skill }: { orgId: string; skill: Skill }) {
  const detail = useSkill(orgId, skill.id)

  return (
    <DetailList
      items={[
        { label: 'ID', value: skill.id, mono: true },
        { label: 'Revision ID', value: skill.revision_id, mono: true },
        { label: 'Description', value: skill.description },
        { label: 'Files', value: detail.data?.files?.length },
        { label: 'Created', value: formatDateTime(skill.created_at) },
        { label: 'Updated', value: formatDateTime(skill.updated_at) },
      ]}
    />
  )
}
