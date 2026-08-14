import type { OmnaraClient } from '@omnara/sdk'
import { createContext, type ReactNode, use } from 'react'

const OmnaraClientContext = createContext<OmnaraClient | undefined>(undefined)

export function OmnaraClientProvider({
  client,
  children,
}: {
  client: OmnaraClient
  children: ReactNode
}) {
  return <OmnaraClientContext value={client}>{children}</OmnaraClientContext>
}

export function useOmnaraClient(): OmnaraClient {
  const client = use(OmnaraClientContext)
  if (!client) {
    throw new Error('useOmnaraClient must be used within an OmnaraClientProvider')
  }
  return client
}
