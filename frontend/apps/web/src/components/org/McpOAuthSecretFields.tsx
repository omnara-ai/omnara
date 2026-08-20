import { useState } from 'react'

import { ChevronDown } from '@/components/icons'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import type { McpOAuthSecretFormSecret } from './CreateSecretDialogState'

export function McpOAuthSecretFields({
  value,
  onChange,
}: {
  value: McpOAuthSecretFormSecret
  onChange: (patch: Partial<McpOAuthSecretFormSecret>) => void
}) {
  const [advancedOpen, setAdvancedOpen] = useState(false)

  return (
    <>
      <Field>
        <FieldLabel htmlFor="mcp-server-url">MCP Server URL</FieldLabel>
        <Input
          id="mcp-server-url"
          required
          type="url"
          value={value.serverUrl}
          autoComplete="off"
          placeholder="https://mcp.example.com"
          onChange={(event) => {
            onChange({ serverUrl: event.target.value })
          }}
        />
      </Field>
      <div className="border-border rounded-md border">
        <button
          type="button"
          className="text-foreground hover:bg-muted/60 flex w-full items-center justify-between gap-3 rounded-md px-3 py-2 text-left text-sm font-medium transition-colors"
          aria-expanded={advancedOpen}
          aria-controls="mcp-oauth-advanced"
          onClick={() => {
            setAdvancedOpen((open) => !open)
          }}
        >
          Advanced OAuth
          <ChevronDown
            className={`size-4 shrink-0 transition-transform ${advancedOpen ? 'rotate-180' : ''}`}
          />
        </button>
        {advancedOpen && (
          <div id="mcp-oauth-advanced" className="border-border grid gap-4 border-t p-3">
            <Field>
              <FieldLabel htmlFor="mcp-oauth-client-id">Client ID</FieldLabel>
              <Input
                id="mcp-oauth-client-id"
                value={value.clientId}
                autoComplete="off"
                placeholder="00000000-0000-0000-0000-000000000000"
                onChange={(event) => {
                  onChange({ clientId: event.target.value })
                }}
              />
              <FieldDescription>
                Used when the authorization server requires a registered OAuth client.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="mcp-oauth-client-secret">Client secret</FieldLabel>
              <Input
                id="mcp-oauth-client-secret"
                type="password"
                value={value.clientSecret ?? ''}
                autoComplete="new-password"
                onChange={(event) => {
                  onChange({ clientSecret: event.target.value })
                }}
              />
            </Field>
          </div>
        )}
      </div>
    </>
  )
}
