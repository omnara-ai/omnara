import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import type { McpOAuthSecretFormSecret } from './CreateSecretDialogState'
import { McpOAuthClientFields } from './McpOAuthClientFields'

export function McpOAuthSecretFields({
  value,
  onChange,
}: {
  value: McpOAuthSecretFormSecret
  onChange: (patch: Partial<McpOAuthSecretFormSecret>) => void
}) {
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
      <McpOAuthClientFields
        clientId={value.clientId}
        clientSecret={value.clientSecret ?? ''}
        onChange={onChange}
      />
    </>
  )
}
