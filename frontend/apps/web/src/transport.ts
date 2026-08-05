import { createOmnaraClient } from '@omnara/sdk'
import { cookieCsrf } from '@omnara/sdk/browser'

export const omnaraClient = createOmnaraClient({ auth: cookieCsrf(), credentials: 'include' })
