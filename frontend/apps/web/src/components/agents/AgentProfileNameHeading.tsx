import { useUpdateAgentProfile } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { CheckIcon, PencilIcon, XIcon } from 'lucide-react'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'

export function AgentProfileNameHeading({
  orgId,
  projectId,
  profile,
  canManage,
}: {
  orgId: string
  projectId: string
  profile: AgentProfile
  canManage: boolean
}) {
  const updateProfile = useUpdateAgentProfile(orgId, projectId)
  const [nameDraft, setNameDraft] = useState<string | null>(null)
  const [renameError, setRenameError] = useState('')

  function stopEditing() {
    setNameDraft(null)
    setRenameError('')
  }

  async function submitRename(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = (nameDraft ?? '').trim()
    if (name === '' || name === profile.name) {
      stopEditing()
      return
    }
    try {
      await updateProfile.mutateAsync({ agentProfileID: profile.id, name })
      stopEditing()
    } catch (err) {
      setRenameError(err instanceof ApiError ? err.message : 'Could not rename agent profile')
    }
  }

  return (
    <>
      {nameDraft !== null ? (
        <form
          className="flex items-center gap-1"
          onSubmit={(event) => {
            void submitRename(event)
          }}
        >
          <Input
            // eslint-disable-next-line jsx-a11y/no-autofocus -- focus follows the user's explicit edit action
            autoFocus
            aria-label="Profile name"
            value={nameDraft}
            className="border-border focus-visible:border-border -mx-1 h-auto w-96 rounded-none border-0 border-b px-1 py-0 text-2xl font-bold tracking-tight shadow-none focus-visible:ring-0 md:text-2xl dark:bg-transparent"
            onChange={(event) => {
              setNameDraft(event.target.value)
            }}
            onKeyDown={(event) => {
              if (event.key === 'Escape') stopEditing()
            }}
          />
          <Button
            size="icon"
            type="submit"
            variant="ghost"
            aria-label="Save name"
            disabled={updateProfile.isPending}
          >
            {updateProfile.isPending ? <Spinner /> : <CheckIcon />}
          </Button>
          <Button
            size="icon"
            type="button"
            variant="ghost"
            aria-label="Cancel rename"
            className="text-muted-foreground"
            onClick={stopEditing}
          >
            <XIcon />
          </Button>
        </form>
      ) : (
        <div className="flex items-center gap-1">
          <h1 className="text-2xl font-bold tracking-tight">{profile.name}</h1>
          {canManage && (
            <Button
              size="icon"
              variant="ghost"
              aria-label="Rename profile"
              className="text-muted-foreground"
              onClick={() => {
                setNameDraft(profile.name)
              }}
            >
              <PencilIcon />
            </Button>
          )}
        </div>
      )}
      {renameError && <p className="text-destructive text-sm">{renameError}</p>}
    </>
  )
}
