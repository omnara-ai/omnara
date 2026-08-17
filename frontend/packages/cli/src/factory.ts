import { Command, InvalidArgumentError } from 'commander'
import * as z from 'zod'
import type { OmnaraClient } from '@omnara/sdk'
import { updateConfigFile, type CliContext } from './context.ts'
import { deriveFlags, type FlagSpec } from './flags.ts'
import type { FormattedOutput, OutputFormat } from './format.ts'
import {
  canPromptInteractively,
  promptOrgSelection,
  promptProjectSelection,
} from './interactive.ts'
import { CliInputError, renderResult, runCliAction, type RenderValue } from './output.ts'

type SdkOperation = (options: never) => Promise<{ data?: RenderValue }>

type ResponseOf<F extends SdkOperation> = F extends (options: never) => PromiseLike<infer R>
  ? R extends { data?: infer D }
    ? Exclude<D, undefined>
    : never
  : never

interface OperationSchemas {
  path?: z.ZodObject<z.ZodRawShape>
  query?: z.ZodObject<z.ZodRawShape>
  body?: z.ZodType
}

export interface OperationSpec extends OperationSchemas {
  verb: string
  summary: string
  fn: SdkOperation
  positional?: string[]
  format: (data: never) => FormattedOutput
}

export interface CommandGroup {
  name: string
  summary: string
  aliases?: string[]
  operations?: OperationSpec[]
  groups?: CommandGroup[]
}

export function op<F extends SdkOperation>(
  verb: string,
  summary: string,
  fn: F,
  schemas: OperationSchemas & {
    positional?: string[]
    format: OutputFormat<ResponseOf<F>>
  },
): OperationSpec {
  return { verb, summary, fn, ...schemas }
}

interface ContextParam {
  key: string
  option: string
  optionKey: string
  configKey: 'org_id' | 'project_id'
  describe: string
  resolve: (ctx: CliContext) => string | undefined
  prompt: (client: OmnaraClient, path: Record<string, RenderValue>) => Promise<string>
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

function kebabCase(name: string): string {
  return name
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .replaceAll('_', '-')
    .toLowerCase()
}

function parseJsonFlag(raw: string): RenderValue {
  try {
    return JSON.parse(raw)
  } catch {
    throw new InvalidArgumentError('expects valid JSON')
  }
}

function registerFlag(command: Command, spec: FlagSpec): void {
  const required = spec.required ? ' (required)' : ''
  const description = `${spec.description}${required}`
  switch (spec.kind) {
    case 'boolean':
      command.option(`--${spec.flag}`, description)
      break
    case 'number':
      command.option(`--${spec.flag} <number>`, description, Number)
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
  options: Record<string, RenderValue>,
): Record<string, RenderValue> {
  const values: Record<string, RenderValue> = {}
  for (const spec of specs) {
    const value = options[spec.optionKey]
    if (value !== undefined) values[spec.key] = value
  }
  return values
}

function parseWithSchema(schema: z.ZodType, value: RenderValue, label: string): RenderValue {
  const result = schema.safeParse(value)
  if (!result.success) {
    throw new CliInputError(`invalid ${label}:\n${z.prettifyError(result.error)}`)
  }
  return result.data as RenderValue
}

interface CallInput {
  client: OmnaraClient
  path?: Record<string, RenderValue>
  query?: Record<string, RenderValue>
  body?: RenderValue
}

async function callOperation(spec: OperationSpec, input: CallInput): Promise<RenderValue> {
  const result = await spec.fn(input as never)
  return result.data
}

function isPaginatedList(spec: OperationSpec): boolean {
  return spec.query?.shape.cursor !== undefined
}

async function fetchAllPages(
  spec: OperationSpec,
  input: CallInput,
): Promise<RenderValue> {
  const rows: RenderValue[] = []
  let cursor: string | undefined
  for (;;) {
    const query = { ...input.query, ...(cursor === undefined ? {} : { cursor }) }
    const page = (await callOperation(spec, { ...input, query })) as {
      data: RenderValue[]
      next_cursor: string | null
    }
    rows.push(...page.data)
    if (page.next_cursor === null) break
    cursor = page.next_cursor
  }
  return { data: rows, next_cursor: null }
}

function registerOperation(parent: Command, ctx: CliContext, spec: OperationSpec): void {
  const command = parent.command(spec.verb).description(spec.summary)
  const pathParams = spec.path ? Object.keys(spec.path.shape) : []
  const contextParams = CONTEXT_PARAMS.filter(
    (context) =>
      pathParams.includes(context.key) && !(spec.positional ?? []).includes(context.key),
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
      const options = command.optsWithGlobals<Record<string, RenderValue>>()
      const input: CallInput = { client: ctx.client }
      if (spec.path) {
        const path: Record<string, RenderValue> = {}
        positionalParams.forEach((param, index) => {
          path[param] = args[index]
        })
        for (const context of contextParams) {
          let value = options[context.optionKey] ?? context.resolve(ctx)
          if (value === undefined && canPromptInteractively()) {
            value = await context.prompt(ctx.client, path)
            updateConfigFile({ [context.configKey]: value })
            console.error(
              `saved ${context.configKey}=${String(value)} as your default (change with omnara context)`,
            )
          }
          if (value === undefined) {
            throw new CliInputError(`missing ${context.optionKey}: ${context.describe}`)
          }
          path[context.key] = value
        }
        input.path = parseWithSchema(spec.path, path, 'arguments') as Record<string, RenderValue>
      }
      if (spec.query) {
        const query = collectFlagValues(queryFlags, options)
        input.query = parseWithSchema(spec.query, query, 'query flags') as Record<string, RenderValue>
      }
      if (spec.body) {
        const base = typeof options.body === 'object' && options.body !== null ? options.body : {}
        const body = { ...base, ...collectFlagValues(bodyFlags, options) }
        input.body = parseWithSchema(spec.body, body, 'request body')
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
