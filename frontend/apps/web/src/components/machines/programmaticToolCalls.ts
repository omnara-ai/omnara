/**
 * The trust boundary behind the machine and pool programmatic tool call toggles. Agents bound to
 * a machine run commands as the same OS user, so the flag is shared by every agent on it.
 */
export const programmaticToolCallsWarning =
  'Agents on the same machine run as the same OS user, so enabling this lets any of them call every bound agent’s opted-in tools. Only enable it where those agents trust each other.'
