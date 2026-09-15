import type {
  ChannelConnectorAppConfiguration,
  ChannelConnectorCapability,
  ChannelConnectorEventReceipt,
  ChannelConnectorInstallationConfiguration,
  ChannelConnectorRuntimeUnit,
  ChannelInboundEventRequest,
  ChannelInboundEventResponse,
  ResolveChannelConnectorInteractionRequest,
  ResolveChannelConnectorInteractionResponse,
} from '@omnara/sdk'
import type { StateAdapter } from 'chat'

export type GatewayLogFields = Record<string, string | number | boolean | null | undefined>

export interface GatewayLogger {
  debug(message: string, fields?: GatewayLogFields): void
  error(message: string, fields?: GatewayLogFields): void
  info(message: string, fields?: GatewayLogFields): void
  warn(message: string, fields?: GatewayLogFields): void
}

export type GatewayAppConfiguration = ChannelConnectorAppConfiguration

export type GatewayInstallationConfiguration = ChannelConnectorInstallationConfiguration

export type ProviderCapability = ChannelConnectorCapability

export interface RuntimeCheckpoint {
  checkpoint: ChannelConnectorRuntimeUnit['checkpoint']
  version: number
}

export interface ProviderInboundContext {
  resolveInteraction(
    interactionId: string,
    request: ResolveChannelConnectorInteractionRequest,
  ): Promise<ResolveChannelConnectorInteractionResponse>
  /** A durable acceptance acknowledgement, not completed behavior processing. */
  submitInbound(event: ChannelInboundEventRequest): Promise<ChannelInboundEventResponse>
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

export interface ReceiptBehaviorContext {
  signal: AbortSignal
  deadlineMs: number
}

/** Process an already-verified queued event using this exact scoped lease.
 * Never re-enter handleWebhook, signature verification, or SDK intake dedupe.
 */
export type ReceiptBehavior = (
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: ReceiptBehaviorContext,
) => Promise<void>

/** retryable asserts replay of the entire behavior is safe. Unknown outcomes
 * must not use it; generic/unclassified exceptions terminalize the receipt.
 */
export class ReceiptBehaviorError extends Error {
  constructor(readonly retryable: boolean) {
    super(
      retryable
        ? 'channel receipt behavior can retry safely'
        : 'channel receipt behavior failed permanently',
    )
    this.name = 'ReceiptBehaviorError'
  }
}

export interface ProviderRuntime {
  close: () => Promise<void>
  handleWebhook: (request: Request, context: ProviderWebhookContext) => Promise<Response>
  processReceipt?: ReceiptBehavior
  runUnit?: (unit: ChannelConnectorRuntimeUnit, context: RuntimeUnitContext) => Promise<void>
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
