import type { PersonalAccessToken } from './generated/types.gen'

const cliLoginTokenPrefix = 'Omnara CLI'
const cliLoginTokenHostSeparator = ' on '

export function cliLoginTokenName(hostName?: string): string {
  if (hostName === undefined) return cliLoginTokenPrefix
  return `${cliLoginTokenPrefix}${cliLoginTokenHostSeparator}${hostName}`
}

export function isCliLoginToken(token: PersonalAccessToken): boolean {
  return token.revoked_at == null && token.name.startsWith(cliLoginTokenPrefix)
}

export function cliLoginTokenHost(token: PersonalAccessToken): string {
  const rest = token.name.slice(cliLoginTokenPrefix.length)
  if (!rest.startsWith(cliLoginTokenHostSeparator)) return token.name
  const host = rest.slice(cliLoginTokenHostSeparator.length).trim()
  return host === '' ? token.name : host
}
