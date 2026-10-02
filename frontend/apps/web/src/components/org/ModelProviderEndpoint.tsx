import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import {
  apiFormatLabel,
  apiFormatOptions,
  awsRegionPattern,
  baseUrlPattern,
  bedrockAPIOption,
  bedrockAPIOptions,
  bedrockAuthOption,
  bedrockAuthOptions,
  type CreateModelProviderFormValues,
} from './CreateModelProviderDialogState'

type EndpointChange = (patch: Partial<CreateModelProviderFormValues>) => void

/**
 * Endpoint settings shown under Advanced: Bedrock's region, API, and auth, or the base URL
 * and API format for every other provider, prefilled with the provider's own endpoint.
 */
export function ModelProviderEndpointSettings({
  values,
  onChange,
}: {
  values: CreateModelProviderFormValues
  onChange: EndpointChange
}) {
  if (values.provider === 'bedrock') {
    return <BedrockProviderFields values={values} onChange={onChange} />
  }
  return <CustomProviderFields values={values} onChange={onChange} />
}

function BedrockProviderFields({
  values,
  onChange,
}: {
  values: CreateModelProviderFormValues
  onChange: EndpointChange
}) {
  const regionValid = awsRegionPattern.test(values.region.trim())
  const sigv4 = values.bedrockAuth === 'sigv4'

  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel htmlFor="mp-bedrock-api">API and endpoint</FieldLabel>
        <Select
          value={values.bedrockAPI}
          onValueChange={(value) => {
            const option = bedrockAPIOptions.find((candidate) => candidate.value === value)
            if (!option) return
            onChange({ bedrockAPI: option.value })
          }}
        >
          <SelectTrigger id="mp-bedrock-api" className="w-full">
            <SelectValue>{bedrockAPIOption(values.bedrockAPI).label}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {bedrockAPIOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>Use the endpoint listed for the model by AWS.</FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="mp-bedrock-auth">Authentication</FieldLabel>
        <Select
          value={values.bedrockAuth}
          onValueChange={(value) => {
            const option = bedrockAuthOptions.find((candidate) => candidate.value === value)
            if (!option) return
            onChange({ bedrockAuth: option.value, secretId: '' })
          }}
        >
          <SelectTrigger id="mp-bedrock-auth" className="w-full">
            <SelectValue>{bedrockAuthOption(values.bedrockAuth).label}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {bedrockAuthOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field>
        <FieldLabel htmlFor="mp-region">AWS region</FieldLabel>
        <Input
          id="mp-region"
          required
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          pattern={awsRegionPattern.source}
          value={values.region}
          placeholder="us-west-2"
          aria-invalid={!regionValid}
          onChange={(event) => {
            onChange({ region: event.target.value })
          }}
        />
        <FieldDescription>
          {!regionValid
            ? 'Enter an AWS region such as us-west-2.'
            : sigv4
              ? 'The AWS region used to sign model requests.'
              : 'The region where your Bedrock API key was generated.'}
        </FieldDescription>
      </Field>
    </div>
  )
}

function CustomProviderFields({
  values,
  onChange,
}: {
  values: CreateModelProviderFormValues
  onChange: EndpointChange
}) {
  const baseUrlValid = baseUrlPattern.test(values.baseUrl.trim())

  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel htmlFor="mp-api-format">API format</FieldLabel>
        <Select
          value={values.apiFormat}
          onValueChange={(value) => {
            const option = apiFormatOptions.find((candidate) => candidate.value === value)
            if (!option) return
            onChange({ apiFormat: option.value })
          }}
        >
          <SelectTrigger id="mp-api-format" className="w-full">
            <SelectValue>{apiFormatLabel(values.apiFormat)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {apiFormatOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>The wire protocol the endpoint speaks.</FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="mp-base-url">Base URL</FieldLabel>
        <Input
          id="mp-base-url"
          required
          type="url"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          value={values.baseUrl}
          placeholder="https://api.example.com/v1"
          aria-invalid={values.baseUrl !== '' && !baseUrlValid}
          onChange={(event) => {
            onChange({ baseUrl: event.target.value })
          }}
        />
        <FieldDescription>
          {baseUrlValid
            ? 'A public HTTPS endpoint. The request path defaults from the API format.'
            : 'Enter a URL such as https://api.example.com/v1.'}
        </FieldDescription>
      </Field>
    </div>
  )
}
