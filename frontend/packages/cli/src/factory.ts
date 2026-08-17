import type { OmnaraClient } from '@omnara/sdk'
import { type Command, InvalidArgumentError } from 'commander'
import * as z from 'zod'

import { type CliContext, updateConfigFile } from './context.ts'
import { deriveFlags, type FlagSpec, kebabCase } from './flags.ts'
import type { OutputFormat } from './format.ts'
import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'
import { CliInputError, renderResult, runCliAction } from './output.ts'

type SdkOperation = (options: never) => Promise<{ data?: unknown }>

type ResponseOf<F extends SdkOperation> = F extends (options: never) => PromiseLike<infer R>
  ? R extends { data?: infer D }
    ? Exclude<D, undefined>
    : never
  : never

export interface OperationSpec<Response = never, ParsedBody = never> {
  verb: string
  summary: string
  fn: SdkOperation
  path?: z.ZodObject<z.ZodRawShape>
  query?: z.ZodObject<z.ZodRawShape>
  body?: z.ZodType
  transformBody?: (body: ParsedBody) => unknown
  positional?: string[]
  format: OutputFormat<Response>
}

export interface CommandGroup {
  name: string
  summary: string
  aliases?: string[]
  operations?: OperationSpec[]
  groups?: CommandGroup[]
}

export function op<F extends SdkOperation, B extends z.ZodType = z.ZodNever>(
  spec: Omit<OperationSpec<ResponseOf<F>, z.output<B>>, 'fn' | 'body'> & { fn: F; body?: B },
): OperationSpec {
  return spec
}

interface ContextParam {
  key: string
  option: string
  optionKey: string
  configKey: 'org_id' | 'project_id'
  describe: string
  resolve: (ctx: CliContext) => string | undefined
  prompt: (client: OmnaraClient, path: Record<string, unknown>) => Promise<string>
}

const CONTEXT_PARAMS: ContextParam[] = [
  {
    key: 'orgID',
    option: '--org <org-id>',
    optionKey: 'org',
    configKey: 'org_id',
    describe: 'pass --org or set OMNARA_ORG_ID',
    resolve: (ctx) => ctx.defaultOrgId,
    prompt: (client) => promptOrgSelection(client),
  },
  {
    key: 'projectID',
    option: '--project <project-id>',
    optionKey: 'project',
    configKey: 'project_id',
    describe: 'pass --project or set OMNARA_PROJECT_ID',
    resolve: (ctx) => ctx.defaultProjectId,
    prompt: (client, path) => {
      const orgId = path.orgID
      if (typeof orgId !== 'string') {
        throw new CliInputError('cannot select a project before an organization is set')
      }
      return promptProjectSelection(client, orgId)
    },
  },
]

function parseJsonFlag(raw: string): unknown {
  try {
    const parsed: unknown = JSON.parse(raw)
    return parsed
  } catch {
    throw new InvalidArgumentError('expects valid JSON')
  }
}

const NUMBER_PATTERN = /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/

function parseNumberFlag(raw: string): number {
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

function collectFlagValues(
  specs: FlagSpec[],
  options: Record<string, unknown>,
): Record<string, unknown> {
  const values: Record<string, unknown> = {}
  for (const spec of specs) {
    const value = options[spec.optionKey]
    if (value !== undefined) values[spec.key] = value
  }
  return values
}

function parseWithSchema<S extends z.ZodType>(
  schema: S,
  value: unknown,
  label: string,
): z.output<S> {
  const result = schema.safeParse(value)
  if (!result.success) {
    throw new CliInputError(`invalid ${label}:\n${z.prettifyError(result.error)}`)
  }
  return result.data
}

interface CallInput {
  client: OmnaraClient
  path?: Record<string, unknown>
  query?: Record<string, unknown>
  body?: unknown
}

async function callOperation(spec: OperationSpec, input: CallInput): Promise<unknown> {
  const result = await spec.fn(input as never)
  return result.data
}

function isPaginatedList(spec: OperationSpec): boolean {
  return spec.query?.shape.cursor !== undefined
}

const zListPage = z.object({
  data: z.array(z.unknown()),
  next_cursor: z.string().nullable(),
})

async function fetchAllPages(spec: OperationSpec, input: CallInput): Promise<unknown> {
  const rows: unknown[] = []
  let cursor: string | undefined
  for (;;) {
    const query = { ...input.query, ...(cursor === undefined ? {} : { cursor }) }
    const page = zListPage.parse(await callOperation(spec, { ...input, query }))
    rows.push(...page.data)
    if (page.next_cursor === null) break
    cursor = page.next_cursor
  }
  return { data: rows, next_cursor: null }
}

const zBodyObject = z.looseObject({})

function saveContextDefault(configKey: 'org_id' | 'project_id', value: string): void {
  try {
    updateConfigFile({ [configKey]: value })
    console.error(`saved ${configKey}=${value} as your default (change with omnara context)`)
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    console.error(`warning: could not save ${configKey} as your default: ${reason}`)
  }
}

function registerOperation(parent: Command, ctx: CliContext, spec: OperationSpec): void {
  const command = parent.command(spec.verb).description(spec.summary)
  const pathParams = spec.path ? Object.keys(spec.path.shape) : []
  const contextParams = CONTEXT_PARAMS.filter(
    (context) => pathParams.includes(context.key) && !(spec.positional ?? []).includes(context.key),
  )
  const positionalParams = pathParams.filter(
    (param) => !contextParams.some((context) => context.key === param),
  )
  for (const param of positionalParams) command.argument(`<${kebabCase(param)}>`)
  for (const context of contextParams) {
    command.option(context.option, `defaults from context (${context.describe})`)
  }
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
  if (isPaginatedList(spec)) command.option('--all', 'follow cursors and fetch every page')
  command.option('--json', 'print the raw JSON response')
  command.action(async (...args: string[]) => {
    await runCliAction(async () => {
      const options = command.opts<Record<string, unknown>>()
      const input: CallInput = { client: ctx.client }
      if (spec.path) {
        const path: Record<string, unknown> = {}
        positionalParams.forEach((param, index) => {
          path[param] = args[index]
        })
        let usedExplicitOverride = false
        for (const context of contextParams) {
          const explicitOption = options[context.optionKey]
          const explicit = typeof explicitOption === 'string' ? explicitOption : undefined
          usedExplicitOverride = usedExplicitOverride || explicit !== undefined
          let value = explicit ?? context.resolve(ctx)
          if (value === undefined && canPromptInteractively()) {
            value = await context.prompt(ctx.client, path)
            if (!usedExplicitOverride) saveContextDefault(context.configKey, value)
          }
          if (value === undefined) {
            throw new CliInputError(`missing ${context.optionKey}: ${context.describe}`)
          }
          path[context.key] = value
        }
        input.path = parseWithSchema(spec.path, path, 'arguments')
      }
      if (spec.query) {
        const query = collectFlagValues(queryFlags, options)
        input.query = parseWithSchema(spec.query, query, 'query flags')
      }
      if (spec.body) {
        const base =
          options.body === undefined ? {} : parseWithSchema(zBodyObject, options.body, '--body')
        const body = { ...base, ...collectFlagValues(bodyFlags, options) }
        const parsed = parseWithSchema(spec.body, body, 'request body')
        input.body = spec.transformBody ? spec.transformBody(parsed as never) : parsed
      }
      const data =
        options.all === true && isPaginatedList(spec)
          ? await fetchAllPages(spec, input)
          : await callOperation(spec, input)
      if (options.json === true) {
        renderResult(data, true)
        return
      }
      const formatted = spec.format(data as never)
      renderResult(formatted.value, false, { columns: formatted.columns })
    })
  })
}

export function registerGroup(program: Command, ctx: CliContext, group: CommandGroup): void {
  const groupCommand = program
    .command(group.name)
    .aliases(group.aliases ?? [])
    .description(group.summary)
  for (const operation of group.operations ?? []) registerOperation(groupCommand, ctx, operation)
  for (const subgroup of group.groups ?? []) registerGroup(groupCommand, ctx, subgroup)
}
