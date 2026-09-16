import type {
  ChannelConnectorAppConfiguration,
  ChannelConnectorCapability,
  ChannelConnectorControlReceipt,
  ChannelConnectorEventReceipt,
  ChannelConnectorInstallationConfiguration,
  ChannelConnectorRuntimeUnit,
  ChannelInboundEventRequest,
  ChannelInboundEventResponse,
  CompleteChannelConnectorControlEventRequest,
  ResolveChannelConnectorInteractionRequest,
  ResolveChannelConnectorInteractionResponse,
} from '@omnara/sdk'

export type ControlCompletion = Omit<
  CompleteChannelConnectorControlEventRequest,
  'lease_token' | 'lease_generation'
>

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
  submitInbound(
    event: ChannelInboundEventRequest,
    signal?: AbortSignal,
  ): Promise<ChannelInboundEventResponse>
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

export interface ControlReceiptBehaviorContext extends ReceiptBehaviorContext {
  reserveWorkBytes: (bytes: number) => ProviderWorkReservation
}

/** Re-observe verified provider state using this app-scoped receipt and its
 * confirmed progress. Control recovery never performs provider message actions.
 * After confirmed partial progress, return retry with last_installation_id;
 * an uncaught throw records no prefix checkpoint. Allocate per-claim work via
 * context.reserveWorkBytes; factory reservations belong to long-lived global
 * app/cache work and do not enforce the control worker's child budget.
 */
export type ControlReceiptBehavior = (
  receipt: Readonly<ChannelConnectorControlReceipt>,
  context: ControlReceiptBehaviorContext,
) => Promise<ControlCompletion>

/** Process an already-verified queued event using this exact scoped lease.
 * Never re-enter handleWebhook, signature verification, or SDK intake dedupe.
 * Verified inbound work can replay after shutdown, deadline or lease expiry,
 * including after partial admission. Use core's receipt/semantic idempotency
 * for durable input effects and honor the supplied signal. Outgoing provider
 * sends retain their separate operation retry/unknown-outcome contract.
 */
export type ReceiptBehavior = (
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: ReceiptBehaviorContext,
) => Promise<void>

/** Explicit permanent failures terminalize incoming work. Message consumers
 * bound retries and terminalize other unclassified errors; control consumers
 * retry unknown failures of idempotent state observations. Interruption retries.
 */
export class ReceiptBehaviorError extends Error {
  constructor(
    readonly retryable: boolean,
    /** Optional provider minimum; invalid supplied hints fail closed in message consumers. */
    readonly retryAfterMs?: number,
  ) {
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
  processControlReceipt?: ControlReceiptBehavior
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
}

export interface ProviderFactory {
  readonly connectorKey: string
  readonly provider: string
  /** Optional response budget from HTTP entry, including app acquisition and body reads. */
  readonly webhookTimeoutMs?: number
  /** Optional raw webhook body limit, enforced before buffering or adapter invocation. */
  readonly webhookBodyLimitBytes?: number
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
