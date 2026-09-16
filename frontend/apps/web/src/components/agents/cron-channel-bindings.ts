import type { AttachAgentChannelRequest, ChannelGrants } from '@omnara/sdk'

function hasGrant(grants: ChannelGrants) {
  return grants.read || grants.send || grants.receive
}

export function channelBindingError(binding: AttachAgentChannelRequest) {
  if (!hasGrant(binding.grants)) return 'Choose at least one permission for this channel.'
  if (binding.reply_channel_grants) {
    if (!binding.grants.send) return 'Enable Send on this channel to allow reply threads.'
    if (!hasGrant(binding.reply_channel_grants))
      return 'Choose at least one permission for reply threads.'
  }
  return ''
}

export function cronChannelBindingsValid(bindings: AttachAgentChannelRequest[]) {
  return (
    bindings.length <= 64 &&
    new Set(bindings.map((binding) => binding.channel_id)).size === bindings.length &&
    bindings.every((binding) => binding.channel_id !== '' && !channelBindingError(binding))
  )
}

function sameGrants(a: ChannelGrants | undefined, b: ChannelGrants | undefined) {
  return a?.read === b?.read && a?.send === b?.send && a?.receive === b?.receive
}

export function sameCronChannelBindings(
  a: AttachAgentChannelRequest[],
  b: AttachAgentChannelRequest[],
) {
  return (
    a.length === b.length &&
    a.every((binding) => {
      const other = b.find((candidate) => candidate.channel_id === binding.channel_id)
      return (
        other !== undefined &&
        sameGrants(binding.grants, other.grants) &&
        sameGrants(binding.reply_channel_grants, other.reply_channel_grants)
      )
    })
  )
}
