import type { OmnaraClient } from '@omnara/sdk'
import { type Command, InvalidArgumentError } from 'commander'
import * as z from 'zod'

import type { CliConfig } from './config.ts'
import type { ConfigStore } from './config-file.ts'
import { deriveFlags, type FlagSpec, kebabCase } from './flags.ts'
import type { OutputFormat } from './format.ts'
import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'
import { CliInputError, renderResult, runCliAction, zOptionalRenderValue } from './output.ts'
import { createFlowReporter, type FlowReporter } from './reporter.ts'

const zFlagValue = z.json()
type FlagValue = z.output<typeof zFlagValue>
const zFlagObject = z.record(z.string(), zFlagValue)
type FlagObject = z.output<typeof zFlagObject>
type CommandOptions = Record<string, FlagValue | undefined>
type PathValues = Record<string, string>

const zNoParams = z.object({})

interface CallInput<Path, Query, Body> {
  client?: OmnaraClient
  path?: Path
  query?: Partial<Query>
  body?: Body
}

type SdkOperation<Response, Path, Query, Body> = {
  call(options?: CallInput<Path, Query, Body>): Promise<{ data: Response }>
}['call']

type BodyOf<F> = F extends (options: infer O) => PromiseLike<{ data: unknown }>
  ? NonNullable<O> extends { body?: infer B }
    ? B
    : never
  : never

type ResponseOf<F> = F extends (options: never) => PromiseLike<infer R>
  ? R extends { data?: infer D }
    ? Exclude<D, undefined>
    : never
  : never

export interface TransformBodyContext<Path> {
  client: OmnaraClient
  path: Path
}

interface OperationRunContext {
  client: OmnaraClient
  apiUrl: string
  path: PathValues
  query: FlagObject
  body: FlagObject
  asJson: boolean
}

export interface OperationSpec {
  type: 'op'
  verb: string
  summary: string
  path?: z.ZodObject
  query?: z.ZodObject
  body?: z.ZodType
  positional?: string[]
  run: (context: OperationRunContext) => Promise<void>
}

export interface FlowContext<Path, Body> {
  client: OmnaraClient
  apiUrl: string
  path: Path
  body: Body
  report: FlowReporter
}

interface FlowInput {
  client: OmnaraClient
  apiUrl: string
  path: PathValues
  body: FlagObject
}

export interface FlowSpec {
  type: 'flow'
  verb: string
  summary: string
  aliases?: string[]
  path: z.ZodObject
  body: z.ZodType
  execute: (input: FlowInput) => Promise<void>
}

export interface CustomSpec {
  type: 'custom'
  register: (parent: Command, config: CliConfig) => void
}

export type CommandSpec = OperationSpec | FlowSpec | CustomSpec

export interface CommandGroup {
  name: string
  summary: string
  aliases?: string[]
  operations?: CommandSpec[]
  groups?: CommandGroup[]
}

interface OperationBase<F, P extends z.ZodObject, Q extends z.ZodObject> {
  verb: string
  summary: string
  fn: F
  path?: P
  query?: Q
  positional?: string[]
  format: OutputFormat<ResponseOf<F>>
}

type OperationBody<F, P extends z.ZodObject, B extends z.ZodType> =
  | { body?: z.ZodType<BodyOf<F>>; transformBody?: undefined }
  | {
      path: P
      body: B
      transformBody: (
        body: z.output<B>,
        context: TransformBodyContext<z.output<P>>,
      ) => BodyOf<F> | Promise<BodyOf<F>>
    }

export function op<
  F extends SdkOperation<ResponseOf<F>, z.output<P>, z.output<Q>, BodyOf<F>>,
  P extends z.ZodObject = typeof zNoParams,
  Q extends z.ZodObject = typeof zNoParams,
  B extends z.ZodType = z.ZodType<BodyOf<F>>,
>(spec: OperationBase<F, P, Q> & OperationBody<F, P, B>): OperationSpec {
  return {
    type: 'op',
    verb: spec.verb,
    summary: spec.summary,
    path: spec.path,
    query: spec.query,
    body: spec.body,
    positional: spec.positional,
    run: async (context) => {
      const input: CallInput<z.output<P>, z.output<Q>, BodyOf<F>> = { client: context.client }
      if (spec.transformBody !== undefined) {
        const path = parseWithSchema(spec.path, context.path, 'arguments')
        input.path = path
        input.body = await spec.transformBody(
          parseWithSchema(spec.body, context.body, 'request body'),
          { client: context.client, path },
        )
      } else {
        if (spec.path !== undefined) {
          input.path = parseWithSchema(spec.path, context.path, 'arguments')
        }
        if (spec.body !== undefined) {
          input.body = parseWithSchema(spec.body, context.body, 'request body')
        }
      }
      if (spec.query !== undefined) {
        input.query = parseWithSchema(spec.query, context.query, 'query flags')
      }
      const { data } = await spec.fn(input)
      if (context.asJson) {
        renderResult(zOptionalRenderValue.parse(data), true)
        return
      }
      const formatted = spec.format(data, { apiUrl: context.apiUrl })
      renderResult(zOptionalRenderValue.parse(formatted.value), false, {
        columns: formatted.columns,
      })
    },
  }
}

export function flowOp<P extends z.ZodObject, B extends z.ZodType>(spec: {
  verb: string
  summary: string
  aliases?: string[]
  path: P
  body: B
  run: (context: FlowContext<z.output<P>, z.output<B>>) => Promise<void>
}): FlowSpec {
  const { run, ...base } = spec
  return {
    ...base,
    type: 'flow',
    execute: (input) =>
      run({
        client: input.client,
        apiUrl: input.apiUrl,
        path: parseWithSchema(spec.path, input.path, 'arguments'),
        body: parseWithSchema(spec.body, input.body, 'flags'),
        report: createFlowReporter(spec.summary),
      }),
  }
}

interface ConfigParam {
  key: string
  option: string
  optionKey: string
  configKey: 'org_id' | 'project_id'
  describe: string
  resolve: (config: CliConfig, path: PathValues) => string | undefined
  prompt: (config: CliConfig, path: PathValues) => Promise<string>
}

const CONFIG_PARAMS: ConfigParam[] = [
  {
    key: 'orgID',
    option: '--org <org-id>',
    optionKey: 'org',
    configKey: 'org_id',
    describe: 'pass --org or set OMNARA_ORG_ID',
    resolve: (config) => config.defaultOrgId,
    prompt: (config) => promptOrgSelection(config.client, config.issuerUrl),
  },
  {
    key: 'projectID',
    option: '--project <project-id>',
    optionKey: 'project',
    configKey: 'project_id',
    describe: 'pass --project or set OMNARA_PROJECT_ID',
    resolve: (config, path) =>
      path.orgID === undefined || path.orgID === config.defaultOrgId
        ? config.defaultProjectId
        : undefined,
    prompt: (config, path) => {
      const orgId = path.orgID
      if (orgId === undefined) {
        throw new CliInputError('cannot select a project before an organization is set')
      }
      return promptProjectSelection(config.client, orgId)
    },
  },
]

export function parseJsonFlag(raw: string): FlagValue {
  try {
    return zFlagValue.parse(JSON.parse(raw))
  } catch {
    throw new InvalidArgumentError('expects valid JSON')
  }
}

const NUMBER_PATTERN = /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/

export function parseNumberFlag(raw: string): number {
  if (!NUMBER_PATTERN.test(raw.trim())) {
    throw new InvalidArgumentError('expects a number')
  }
  return Number(raw)
}

function registerFlag(command: Command, spec: FlagSpec): void {
  const required = spec.required ? ' (required)' : ''
  const description = `${spec.description}${required}`
  switch (spec.kind) {
    case 'boolean':
      command.option(`--${spec.flag}`, description)
      command.option(`--no-${spec.flag}`, `set ${spec.flag} to false`)
      break
    case 'number':
      command.option(`--${spec.flag} <number>`, description, parseNumberFlag)
      break
    case 'stringArray':
      command.option(
        `--${spec.flag} <value>`,
        description,
        (value: string, previous: string[] | undefined) => [...(previous ?? []), value],
      )
      break
    case 'json':
      command.option(`--${spec.flag} <json>`, description, parseJsonFlag)
      break
    default:
      command.option(`--${spec.flag} <value>`, description)
  }
}

function collectFlagValues(specs: FlagSpec[], options: CommandOptions) {
  const root: FlagObject = {}
  const containers = new Map<string, FlagObject>()
  const containerFor = (path: readonly string[]) => {
    const name = path[path.length - 1]
    if (name === undefined) return root
    const pathKey = path.join('.')
    let container = containers.get(pathKey)
    if (container === undefined) {
      container = {}
      containers.set(pathKey, container)
      containerFor(path.slice(0, -1))[name] = container
    }
    return container
  }
  for (const spec of specs) {
    const value = options[spec.optionKey]
    const name = spec.path[spec.path.length - 1]
    if (value === undefined || name === undefined) continue
    containerFor(spec.path.slice(0, -1))[name] = value
  }
  return root
}

export function parseWithSchema<S extends z.ZodType>(
  schema: S,
  value: FlagValue | PathValues,
  label: string,
): z.output<S> {
  const result = schema.safeParse(value)
  if (!result.success) {
    throw new CliInputError(`invalid ${label}:\n${z.prettifyError(result.error)}`)
  }
  return result.data
}

function deepMerge(base: FlagObject, patch: FlagObject): FlagObject {
  const merged = new Map(Object.entries(base))
  for (const [key, value] of Object.entries(patch)) {
    const existing = zFlagObject.safeParse(merged.get(key))
    const incoming = zFlagObject.safeParse(value)
    merged.set(
      key,
      existing.success && incoming.success ? deepMerge(existing.data, incoming.data) : value,
    )
  }
  return Object.fromEntries(merged)
}

function saveConfigDefault(
  store: ConfigStore,
  configKey: 'org_id' | 'project_id',
  value: string,
): void {
  try {
    store.update(
      configKey === 'org_id' ? { org_id: value, project_id: undefined } : { [configKey]: value },
    )
    console.error(`saved ${configKey}=${value} as your default (change with omnara config)`)
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    console.error(`warning: could not save ${configKey} as your default: ${reason}`)
  }
}

export interface PathPlan {
  positionalParams: string[]
  configParams: ConfigParam[]
}

export function planPathParams(path: z.ZodObject | undefined, positional: string[]): PathPlan {
  const pathParams = path ? Object.keys(path.shape) : []
  const configParams = CONFIG_PARAMS.filter(
    (param) => pathParams.includes(param.key) && !positional.includes(param.key),
  )
  return {
    positionalParams: pathParams.filter(
      (name) => !configParams.some((param) => param.key === name),
    ),
    configParams,
  }
}

export function registerPathParams(command: Command, plan: PathPlan): void {
  for (const param of plan.positionalParams) command.argument(`<${kebabCase(param)}>`)
  for (const param of plan.configParams) {
    command.option(param.option, `defaults from config (${param.describe})`)
  }
}

const zExplicitOption = z.string().optional()

export async function resolvePathValues(
  plan: PathPlan,
  args: string[],
  options: CommandOptions,
  config: CliConfig,
): Promise<PathValues> {
  const path: PathValues = {}
  plan.positionalParams.forEach((param, index) => {
    const arg = args[index]
    if (arg !== undefined) path[param] = arg
  })
  let usedExplicitOverride = false
  for (const param of plan.configParams) {
    const explicit = zExplicitOption.parse(options[param.optionKey])
    usedExplicitOverride = usedExplicitOverride || explicit !== undefined
    let value = explicit ?? param.resolve(config, path)
    if (value === undefined && canPromptInteractively()) {
      value = await param.prompt(config, path)
      if (!usedExplicitOverride) saveConfigDefault(config.store, param.configKey, value)
    }
    if (value === undefined) {
      throw new CliInputError(`missing ${param.optionKey}: ${param.describe}`)
    }
    path[param.key] = value
  }
  return path
}

export function registerOperation(parent: Command, config: CliConfig, spec: OperationSpec): void {
  const command = parent.command(spec.verb).description(spec.summary)
  const plan = planPathParams(spec.path, spec.positional ?? [])
  registerPathParams(command, plan)
  const queryFlags = spec.query ? deriveFlags(spec.query) : []
  const bodyFlags = spec.body ? deriveFlags(spec.body) : []
  for (const flag of [...queryFlags, ...bodyFlags]) registerFlag(command, flag)
  if (spec.body) {
    command.option(
      '--body <json>',
      'full request body as JSON; field flags override it',
      parseJsonFlag,
    )
  }
  command.option('--json', 'print the raw JSON response')
  command.action(async (...args: string[]) => {
    await runCliAction(async () => {
      await config.ensureLoggedIn()
      const options = command.opts<CommandOptions>()
      const base =
        options.body === undefined ? {} : parseWithSchema(zFlagObject, options.body, '--body')
      await spec.run({
        client: config.client,
        apiUrl: config.apiUrl,
        path: await resolvePathValues(plan, args, options, config),
        query: collectFlagValues(queryFlags, options),
        body: deepMerge(base, collectFlagValues(bodyFlags, options)),
        asJson: options.json === true,
      })
    })
  })
}

function registerFlow(parent: Command, config: CliConfig, spec: FlowSpec): void {
  const command = parent
    .command(spec.verb)
    .aliases(spec.aliases ?? [])
    .description(spec.summary)
  const plan = planPathParams(spec.path, [])
  registerPathParams(command, plan)
  const bodyFlags = deriveFlags(spec.body)
  for (const flag of bodyFlags) registerFlag(command, flag)
  command.action(async (...args: string[]) => {
    await runCliAction(async () => {
      await config.ensureLoggedIn()
      const options = command.opts<CommandOptions>()
      await spec.execute({
        client: config.client,
        apiUrl: config.apiUrl,
        path: await resolvePathValues(plan, args, options, config),
        body: collectFlagValues(bodyFlags, options),
      })
    })
  })
}

export function registerGroup(program: Command, config: CliConfig, group: CommandGroup): void {
  const groupCommand = program
    .command(group.name)
    .aliases(group.aliases ?? [])
    .description(group.summary)
  for (const spec of group.operations ?? []) {
    if (spec.type === 'op') registerOperation(groupCommand, config, spec)
    else if (spec.type === 'flow') registerFlow(groupCommand, config, spec)
    else spec.register(groupCommand, config)
  }
  for (const subgroup of group.groups ?? []) registerGroup(groupCommand, config, subgroup)
}
