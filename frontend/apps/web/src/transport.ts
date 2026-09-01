import { createOmnaraClient } from '@omnara/sdk'
import { cookieCsrf } from '@omnara/sdk/browser'

export const omnaraClient = createOmnaraClient({
  baseUrl: '/api/v1',
  auth: cookieCsrf(),
  credentials: 'include',
})
