import type {
  ChannelConnectorAppConfiguration,
  ChannelConnectorCapability,
  ChannelConnectorDelivery,
  ChannelConnectorInstallationConfiguration,
  ChannelConnectorRuntimeUnit,
  ChannelInboundEventRequest,
  ResolveChannelConnectorInteractionRequest,
  ResolveChannelConnectorInteractionResponse,
} from '@omnara/sdk'
import type { StateAdapter } from 'chat'

export interface GatewayLogger {
  debug(message: string, fields?: Record<string, unknown>): void
  error(message: string, fields?: Record<string, unknown>): void
  info(message: string, fields?: Record<string, unknown>): void
  warn(message: string, fields?: Record<string, unknown>): void
}

export type GatewayAppConfiguration = ChannelConnectorAppConfiguration

export type GatewayInstallationConfiguration = ChannelConnectorInstallationConfiguration

export type ProviderCapability = ChannelConnectorCapability

export interface ProviderSendResult {
  // The gateway omits invalid or oversized references while retaining the
  // successful delivery outcome. Provider adapters should return at most 2048
  // UTF-8 bytes and must not include NUL.
  providerMessageRef: string
}

export interface ProviderSendContext {
  installation: GatewayInstallationConfiguration
  signal: AbortSignal
}

export interface RuntimeCheckpoint {
  checkpoint: Record<string, unknown>
  version: number
}

export interface ProviderInboundContext {
  resolveInteraction(
    interactionId: string,
    request: ResolveChannelConnectorInteractionRequest,
  ): Promise<ResolveChannelConnectorInteractionResponse>
  submitInbound(event: ChannelInboundEventRequest): Promise<void>
}

export interface RuntimeUnitWorkContext {
  installation?: GatewayInstallationConfiguration
  reserveWorkBytes: (bytes: number) => ProviderWorkReservation
  signal: AbortSignal
  updateCheckpoint: (checkpoint: RuntimeCheckpoint) => void
}

export interface RuntimeUnitContext extends RuntimeUnitWorkContext, ProviderInboundContext {}

export interface ProviderWebhookWorkContext {
  reserveWorkBytes: (bytes: number) => ProviderWorkReservation
  waitUntil: (task: Promise<unknown>) => void
}

export interface ProviderWebhookContext
  extends ProviderWebhookWorkContext, ProviderInboundContext {}

export interface ProviderWorkReservation {
  release: () => void
  resize: (bytes: number) => void
}

export interface ProviderRuntime {
  close: () => Promise<void>
  handleWebhook: (request: Request, context: ProviderWebhookContext) => Promise<Response>
  runUnit?: (unit: ChannelConnectorRuntimeUnit, context: RuntimeUnitContext) => Promise<void>
  send: (
    delivery: ChannelConnectorDelivery,
    context: ProviderSendContext,
  ) => Promise<ProviderSendResult>
}

export interface ProviderFactoryContext {
  configuration: GatewayAppConfiguration
  getInstallation(
    integrationInstallId: string,
    expectedRevision?: number,
  ): Promise<GatewayInstallationConfiguration>
  logger: GatewayLogger
  reserveWorkBytes: (bytes: number) => ProviderWorkReservation
  resolveInstallation(
    externalTenantId: string,
    externalAccountRef: string,
  ): Promise<GatewayInstallationConfiguration>
  signal: AbortSignal
  state: StateAdapter
}

export interface ProviderFactory {
  readonly connectorKey: string
  readonly provider: string
  // Factory implementations must thread lifecycle and per-operation signals
  // into adapter initialization, provider I/O, and media downloads before
  // registering the factory. A timeout that only stops awaiting I/O is not
  // sufficient: the provider request itself must have a finite deadline.
  create: (context: ProviderFactoryContext) => Promise<ProviderRuntime>
}

export type ProviderFactoryRegistry = ReadonlyMap<string, ProviderFactory>

export class ProviderDeliveryError extends Error {
  readonly outcomeUnknown: boolean
  readonly retryAfterMs?: number
  readonly retryable: boolean

  constructor(
    message: string,
    options: { outcomeUnknown?: boolean; retryAfterMs?: number; retryable?: boolean } = {},
  ) {
    super(message)
    this.name = 'ProviderDeliveryError'
    this.outcomeUnknown = options.outcomeUnknown ?? false
    this.retryAfterMs = options.retryAfterMs
    this.retryable = options.retryable ?? false
  }
}

export function providerFactoryKey(connectorKey: string, provider: string): string {
  return `${connectorKey}\u0000${provider}`
}
