import { useRevokePersonalAccessToken } from '@omnara/react'
import type { PersonalAccessToken } from '@omnara/sdk'

import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { errorMessage } from '@/lib/submit-status'

export function PersonalAccessTokenRowActions({ token }: { token: PersonalAccessToken }) {
  const revokeToken = useRevokePersonalAccessToken()
  if (token.revoked_at) return null

  async function revoke() {
    if (!window.confirm(`Revoke the API token “${token.name}”? This cannot be undone.`)) return
    try {
      await revokeToken.mutateAsync(token.id)
    } catch (err) {
      window.alert(errorMessage(err, 'Could not revoke API token'))
    }
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={`Actions for ${token.name}`}>
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem variant="destructive" onSelect={() => void revoke()}>
          Revoke token
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
