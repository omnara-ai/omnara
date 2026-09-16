import {
  type createOmnaraClient,
  type ListChannelConnectorInstallationControlScopesData,
  type ListChannelConnectorInstallationControlScopesResponse,
  schemas,
  sdk,
  type SetChannelConnectorInstallationProviderStateRequest,
  type SetChannelConnectorInstallationProviderStateResponse,
} from '@omnara/sdk'

import type { ProviderWorkReservation } from '../types'
import { ReceiptClientError, receiptFetch } from './receipt-http'
import { receiptFailure } from './requests'

type Client = ReturnType<typeof createOmnaraClient>
export type InstallationControlQuery = ListChannelConnectorInstallationControlScopesData['query']

/** Direct generated-client boundary. The caller owns the request deadline and
 * bounded fanout; this helper creates no retry loop or lifecycle queue.
 * Managed setup writes small verified identities, not arbitrary provider JSON.
 * Keep new provider identity projections within the bounded page response.
 */
export async function listInstallationControlScopes(
  client: Client,
  appId: string,
  query: InstallationControlQuery,
  signal: AbortSignal,
  work?: ProviderWorkReservation,
): Promise<ListChannelConnectorInstallationControlScopesResponse> {
  try {
    signal.throwIfAborted()
    const { data, response } = await sdk.listChannelConnectorInstallationControlScopes({
      client,
      path: { integrationAppID: appId },
      query,
      signal,
      redirect: 'error',
      fetch: receiptFetch(client.getConfig().fetch ?? globalThis.fetch, 1024 * 1024, signal, work),
      responseValidator: (data) =>
        schemas.zListChannelConnectorInstallationControlScopesResponse.safeParse(data).success
          ? Promise.resolve()
          : Promise.reject(new ReceiptClientError('invalid_response')),
    })
    if (response.status !== 200) throw new ReceiptClientError('invalid_response')
    const page = data
    if (
      !Number.isSafeInteger(page.app_configuration_revision) ||
      page.installations.length > (query.limit ?? 50) ||
      new Set(page.installations.map((install) => install.id)).size !== page.installations.length ||
      page.installations.some(
        (install) =>
          install.provider_tenant_id !== query.provider_tenant_id ||
          !Number.isSafeInteger(install.configuration_revision),
      ) ||
      (query.through_installation_id !== undefined &&
        page.through_installation_id !== query.through_installation_id) ||
      (page.through_installation_id === null && page.installations.length > 0) ||
      (page.next_after_installation_id !== null &&
        page.next_after_installation_id !== page.installations.at(-1)?.id)
    )
      throw new ReceiptClientError('invalid_response')
    return page
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

/** A 409 requires a new core AND provider observation. Never refresh a revision
 * here and replay the old state. Lost acknowledgments remain visible to callers.
 */
export async function setInstallationProviderState(
  client: Client,
  appId: string,
  installId: string,
  body: SetChannelConnectorInstallationProviderStateRequest,
  signal: AbortSignal,
): Promise<SetChannelConnectorInstallationProviderStateResponse> {
  try {
    signal.throwIfAborted()
    if (
      !schemas.zSetChannelConnectorInstallationProviderStateRequest.safeParse(body).success ||
      !Number.isSafeInteger(body.expected_app_configuration_revision) ||
      !Number.isSafeInteger(body.expected_configuration_revision + 1)
    )
      throw new ReceiptClientError('invalid_request')
    const { data, response } = await sdk.setChannelConnectorInstallationProviderState({
      client,
      path: { integrationAppID: appId, integrationInstallID: installId },
      body,
      signal,
      redirect: 'error',
      fetch: receiptFetch(client.getConfig().fetch ?? globalThis.fetch, 4096, signal),
      responseValidator: (data) =>
        schemas.zSetChannelConnectorInstallationProviderStateResponse.safeParse(data).success
          ? Promise.resolve()
          : Promise.reject(new ReceiptClientError('invalid_response')),
    })
    if (
      response.status !== 200 ||
      data.state !== body.state ||
      data.configuration_revision !== body.expected_configuration_revision + 1
    )
      throw new ReceiptClientError('invalid_response')
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}
