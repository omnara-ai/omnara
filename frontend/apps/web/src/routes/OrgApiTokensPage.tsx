import { OrgApiKeysSection } from '@/components/api-tokens/OrgApiKeysSection'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrgApiTokensPage() {
  const { activeOrg } = useActiveOrg()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      {canManageOrg(activeOrg.role) ? (
        <OrgApiKeysSection orgId={activeOrg.id} />
      ) : (
        <p role="alert" className="text-muted-foreground text-sm">
          Only organization admins can manage organization API tokens.
        </p>
      )}
    </div>
  )
}
