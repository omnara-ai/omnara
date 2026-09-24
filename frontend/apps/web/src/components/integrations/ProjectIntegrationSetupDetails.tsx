import type { ProjectIntegration } from '@omnara/sdk'
import type { ReactNode } from 'react'
import { z } from 'zod'

import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { ProjectIntegrationPortalSetup } from './ProjectIntegrationPortalSetup'
import { ProjectIntegrationSetupGroup } from './ProjectIntegrationSetupGroup'

interface SetupDetailsProps {
  integration?: ProjectIntegration
  tenant: string
  onTenantChange: (value: string) => void
  savedSecret: string
  children: ReactNode
}

export function GitHubSetupDetails({
  integration,
  tenant,
  onTenantChange,
  account,
  onAccountChange,
  savedSecret,
  children,
}: SetupDetailsProps & { account: string; onAccountChange: (value: string) => void }) {
  const providerTenant = integration?.provider_tenant_id ?? ''
  const providerAccount = integration?.provider_account_ref ?? ''
  return (
    <>
      <ProjectIntegrationSetupGroup
        title="App identity"
        hint="The App ID is on the app’s settings page. The Installation ID is a different number, at the end of its installation URL."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <ProviderTenantField
            label="GitHub App ID"
            providerTenant={providerTenant}
            tenant={tenant}
            savedSecret={savedSecret}
            onTenantChange={onTenantChange}
          />
          <Field>
            <FieldLabel htmlFor="provider-account">Installation ID</FieldLabel>
            <Input
              key={providerAccount}
              id="provider-account"
              name="account"
              inputMode="numeric"
              defaultValue={providerAccount !== '' ? providerAccount : account}
              readOnly={Boolean(providerAccount)}
              required
              pattern="[1-9][0-9]*"
              onChange={(event) => {
                onAccountChange(event.target.value.trim())
              }}
            />
          </Field>
        </div>
      </ProjectIntegrationSetupGroup>
      <ProjectIntegrationSetupGroup
        title="Credentials"
        hint="The private key lets Omnara act as the app. The webhook secret verifies what GitHub sends."
      >
        {children}
      </ProjectIntegrationSetupGroup>
      <ProjectIntegrationSetupGroup
        title="Then, in GitHub"
        hint="Connect here first, then finish setup in your GitHub App’s settings."
      >
        <ProjectIntegrationPortalSetup
          integrationType="github_pr"
          providerId={providerTenant || tenant}
        />
      </ProjectIntegrationSetupGroup>
    </>
  )
}

export function DiscordSetupDetails({
  integration,
  tenant,
  onTenantChange,
  savedSecret,
  children,
}: SetupDetailsProps) {
  const providerTenant = integration?.provider_tenant_id ?? ''
  return (
    <>
      <ProjectIntegrationSetupGroup
        title="1. Application details"
        hint="Copy both values from General Information."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <ProviderTenantField
            label="Discord Application ID"
            providerTenant={providerTenant}
            tenant={tenant}
            savedSecret={savedSecret}
            onTenantChange={onTenantChange}
          />
          <Field>
            <FieldLabel htmlFor="discord-public-key">Public key</FieldLabel>
            <Input
              id="discord-public-key"
              name="publicKey"
              className="font-mono"
              spellCheck={false}
              defaultValue={z.string().catch('').parse(integration?.provider_config.public_key)}
              pattern="[a-fA-F0-9]{64}"
              required
            />
          </Field>
        </div>
      </ProjectIntegrationSetupGroup>
      <ProjectIntegrationSetupGroup
        title="2. Bot token"
        hint="On the Bot page, copy your token (or use Reset Token to create one). Enable Message Content Intent so agents can read replies."
      >
        {children}
      </ProjectIntegrationSetupGroup>
    </>
  )
}

function ProviderTenantField({
  label,
  providerTenant,
  tenant,
  savedSecret,
  onTenantChange,
}: Pick<SetupDetailsProps, 'tenant' | 'savedSecret' | 'onTenantChange'> & {
  label: string
  providerTenant: string
}) {
  return (
    <Field>
      <FieldLabel htmlFor="provider-tenant">{label}</FieldLabel>
      <Input
        key={providerTenant}
        id="provider-tenant"
        name="tenant"
        inputMode="numeric"
        defaultValue={providerTenant !== '' ? providerTenant : tenant}
        readOnly={Boolean(providerTenant || savedSecret)}
        required
        pattern="[1-9][0-9]*"
        onChange={(event) => {
          onTenantChange(event.target.value.trim())
        }}
      />
    </Field>
  )
}
