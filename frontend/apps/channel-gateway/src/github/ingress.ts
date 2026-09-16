import { createHmac, timingSafeEqual } from 'node:crypto'

import { type ChannelConnectorInstallationConfiguration, schemas } from '@omnara/sdk'

import { raceWithAbort } from '../async'
import { ReceiptClientError } from '../core/receipt-http'
import { isCoreNotFoundError } from '../core/requests'
import type {
  ProviderFactoryContext,
  ProviderWebhookContext,
  ProviderWorkReservation,
} from '../types'
import { GatewayAtCapacityError } from '../work-budget'
import { type GitHubLifecycleEvent, githubWebhookBytes, projectGitHubEvent } from './events'
import { githubWorkflowInput } from './input'

export interface GitHubIngressOptions {
  webhookSecret: string
  resolveInstallation(
    tenant: string,
    repository: string,
    signal: AbortSignal,
  ): Promise<ChannelConnectorInstallationConfiguration>
  saveControl(event: GitHubLifecycleEvent, signal: AbortSignal): Promise<void>
}

/** Success for communication means durable core receipt acceptance, never a
 * detached task or an in-memory SDK dedupe entry. Lifecycle control is saved
 * separately under app authority and never reconciled inside the webhook.
 */
export async function receiveGitHubWebhook(
  request: Request,
  factory: ProviderFactoryContext,
  context: ProviderWebhookContext,
  options: GitHubIngressOptions,
): Promise<Response> {
  if (request.method !== 'POST') return new Response(null, { status: 405 })
  const contentType = request.headers.get('content-type')?.split(';')[0]?.trim()
  if (contentType !== 'application/json' || request.headers.has('content-encoding'))
    return new Response(null, { status: 415 })
  const signature = request.headers.get('x-hub-signature-256') ?? ''
  if (!/^sha256=[0-9a-f]{64}$/.test(signature)) return new Response(null, { status: 401 })
  const signal = AbortSignal.any([request.signal, factory.signal])
  let work: ProviderWorkReservation | undefined
  try {
    work = context.reserveWorkBytes(64 * 1024)
    let raw: Buffer
    try {
      raw = await webhookBody(request, signal, (bytes) => {
        work?.resize(Math.max(64 * 1024, bytes * 32))
      })
    } catch (cause) {
      if (cause instanceof WebhookTooLargeError) return new Response(null, { status: 413 })
      throw cause
    }
    const expected = createHmac('sha256', options.webhookSecret).update(raw).digest()
    if (!timingSafeEqual(expected, Buffer.from(signature.slice(7), 'hex')))
      return new Response(null, { status: 401 })
    let projected: ReturnType<typeof projectGitHubEvent>
    try {
      projected = projectGitHubEvent(
        new TextDecoder('utf-8', { fatal: true }).decode(raw),
        request.headers.get('x-github-event') ?? '',
        request.headers.get('x-github-delivery') ?? '',
      )
    } catch {
      return new Response(null, { status: 400 })
    }
    if (!projected) return new Response(null, { status: 204 })
    if ('installationID' in projected) {
      if (projected.appID !== factory.configuration.app.provider_app_ref)
        return new Response(null, { status: 400 })
      await options.saveControl(projected, signal)
      return new Response(null, { status: 202 })
    }
    try {
      githubWorkflowInput(projected)
    } catch {
      return new Response(null, { status: 413 })
    }
    signal.throwIfAborted()
    const install = await options.resolveInstallation(
      projected.installation_id,
      projected.repository.id,
      signal,
    )
    if (
      !schemas.zChannelConnectorInstallationConfiguration.safeParse(install).success ||
      install.integration_app_id !== factory.configuration.app.id ||
      install.app_configuration_revision !== factory.configuration.app.configuration_revision ||
      install.install.provider_tenant_id !== projected.installation_id ||
      install.install.provider_account_ref !== projected.repository.id ||
      install.install.provider_identity.repository_node_id !== projected.repository.node_id
    )
      return new Response(null, { status: 409 })
    signal.throwIfAborted()
    await context.submitInbound(
      {
        integration_install_id: install.install.id,
        event_id: projected.delivery_id,
        payload: projected,
      },
      signal,
    )
    return new Response(null, { status: 202 })
  } catch (cause) {
    if (isCoreNotFoundError(cause)) return new Response(null, { status: 404 })
    if (cause instanceof ReceiptClientError && cause.status === 409)
      return new Response(null, { status: 409 })
    if (cause instanceof GatewayAtCapacityError) return new Response(null, { status: 503 })
    return new Response(null, { status: 503 })
  } finally {
    work?.release()
  }
}

class WebhookTooLargeError extends Error {}

async function webhookBody(request: Request, signal: AbortSignal, charge: (bytes: number) => void) {
  const declared = request.headers.get('content-length')
  if (declared && /^\d+$/.test(declared) && BigInt(declared) > BigInt(githubWebhookBytes))
    throw new WebhookTooLargeError()
  const reader = request.body?.getReader()
  if (!reader) return Buffer.alloc(0)
  const chunks: Uint8Array[] = []
  let bytes = 0
  try {
    for (;;) {
      const part = await raceWithAbort(reader.read(), signal)
      if (part.done) return Buffer.concat(chunks, bytes)
      bytes += part.value.byteLength
      if (bytes > githubWebhookBytes) throw new WebhookTooLargeError()
      charge(bytes)
      chunks.push(part.value)
    }
  } finally {
    void reader.cancel().catch(() => undefined)
  }
}
