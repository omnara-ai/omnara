import { type AuthConnector, listAuthConnectors } from '@omnara/sdk/browser'
import { useQuery } from '@tanstack/react-query'

import { LastUsedBadge } from '@/components/auth/LastUsedBadge'
import { KeyRound } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { FieldSeparator } from '@/components/ui/field'

function GoogleIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden className="size-4">
      <path d="M12.48 10.92v3.28h7.84c-.24 1.84-.853 3.187-1.787 4.133-1.147 1.147-2.933 2.4-6.053 2.4-4.827 0-8.6-3.893-8.6-8.72s3.773-8.72 8.6-8.72c2.6 0 4.507 1.027 5.907 2.347l2.307-2.307C18.747 1.44 16.133 0 12.48 0 5.867 0 .307 5.387.307 12s5.56 12 12.173 12c3.573 0 6.267-1.173 8.373-3.36 2.16-2.16 2.84-5.213 2.84-7.667 0-.76-.053-1.467-.173-2.053H12.48z" />
    </svg>
  )
}

function GitHubIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden className="size-4">
      <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23a11.51 11.51 0 0 1 3.003-.404c1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222 0 1.606-.014 2.898-.014 3.293 0 .322.216.694.825.576C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
    </svg>
  )
}

function ConnectorIcon({ connector }: { connector: AuthConnector }) {
  if (connector.kind === 'github') return <GitHubIcon />
  if (connector.slug === 'google') return <GoogleIcon />
  return <KeyRound className="size-4" aria-hidden />
}

function connectorLoginHref(connector: AuthConnector, returnTo: string) {
  const params = new URLSearchParams({ return_to: returnTo })
  return `${connector.loginURL}?${params}`
}

export function SocialButtons({
  returnTo,
  lastUsedMethod,
}: {
  returnTo: string
  lastUsedMethod?: string
}) {
  const connectorsQuery = useQuery({
    queryKey: ['auth-connectors'],
    queryFn: listAuthConnectors,
  })
  const connectors = (connectorsQuery.data ?? []).filter((connector) => connector.loginURL)

  if (connectorsQuery.isError) {
    return (
      <p role="status" className="text-muted-foreground text-center text-sm">
        Social sign-in is temporarily unavailable. Refresh to try again.
      </p>
    )
  }
  if (connectors.length === 0) return null

  return (
    <div className="flex flex-col gap-6">
      <div className="grid gap-3">
        {connectors.map((connector) => (
          <Button key={connector.slug} asChild variant="outline" className="w-full justify-start">
            <a href={connectorLoginHref(connector, returnTo)}>
              <ConnectorIcon connector={connector} />
              Continue with {connector.displayName}
              {lastUsedMethod === `connector:${connector.slug}` && (
                <LastUsedBadge className="ml-auto" />
              )}
            </a>
          </Button>
        ))}
      </div>
      <FieldSeparator>OR</FieldSeparator>
    </div>
  )
}
