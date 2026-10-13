import { stat, writeFile } from 'node:fs/promises'
import { basename } from 'node:path'

import { FILE_DIGEST_HEADER, MAX_MEMORY_FILE_BYTES, sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import { InvalidArgumentError, Option } from 'commander'
import * as z from 'zod'

import { customOp, parseWithSchema } from './factory.ts'
import { loadMemoryUpload } from './memory-files.ts'
import { CliInputError, renderResult } from './output.ts'

// Exit status used when the remote command timed out or never produced one,
// matching coreutils `timeout`.
export const EXEC_TIMEOUT_EXIT_CODE = 124
const EXEC_NO_STATUS_EXIT_CODE = 1

const zEnvAssignment = z.string().regex(/^[^=\s]+=/, 'expected KEY=VALUE')

function collectEnv(value: string, previous: string[] = []): string[] {
  if (!zEnvAssignment.safeParse(value).success) {
    throw new InvalidArgumentError('expected KEY=VALUE')
  }
  return [...previous, value]
}

export function parseEnvAssignments(values: readonly string[]): Record<string, string> | undefined {
  if (values.length === 0) return undefined
  const env: Record<string, string> = {}
  for (const assignment of values) {
    const index = assignment.indexOf('=')
    env[assignment.slice(0, index)] = assignment.slice(index + 1)
  }
  return env
}

export function execExitCode(result: { exit_code: number | null; timed_out: boolean }): number {
  if (result.timed_out) return EXEC_TIMEOUT_EXIT_CODE
  return result.exit_code ?? EXEC_NO_STATUS_EXIT_CODE
}

const zExecOptions = z.object({
  command: z.string().min(1),
  cwd: z.string().optional(),
  shell: schemas.zExecMachineCommandRequest.shape.shell,
  timeoutSeconds: z.coerce.number().int().min(1).max(3600).optional(),
  env: z.array(z.string()).default([]),
  json: z.boolean().optional(),
})

export const machineExecOp = customOp({
  verb: 'exec',
  summary: 'Run a command on a machine and print its output',
  path: schemas.zExecMachineCommandPath,
  configure: (command) => {
    command
      .requiredOption('--command <command>', 'shell command to run')
      .option('--cwd <dir>', 'working directory on the machine')
      .addOption(
        new Option('--shell <shell>', 'shell that runs the command').choices([
          'default',
          'sh',
          'bash',
          'zsh',
          'powershell',
          'pwsh',
          'cmd',
        ]),
      )
      .option(
        '--timeout-seconds <seconds>',
        'kill the command after this many seconds (default 300)',
      )
      .option('--env <KEY=VALUE>', 'environment variable for the command; repeatable', collectEnv)
      .option('--json', 'print the full result as JSON')
  },
  run: async ({ client, path, options }) => {
    const result = zExecOptions.safeParse(options)
    if (!result.success) {
      throw new CliInputError(`invalid flags:\n${z.prettifyError(result.error)}`)
    }
    const parsed = result.data
    const { data } = await sdk.execMachineCommand({
      client,
      path,
      body: {
        command: parsed.command,
        cwd: parsed.cwd,
        shell: parsed.shell,
        timeout_seconds: parsed.timeoutSeconds,
        env: parseEnvAssignments(parsed.env),
      },
    })
    if (parsed.json === true) {
      renderResult(data, true)
    } else {
      process.stdout.write(data.output)
      if (data.output_truncated) {
        console.error('omnara: output truncated; redirect large output to a file and download it')
      }
      if (data.timed_out) console.error('omnara: command timed out')
      else if (data.exit_code === null && data.error) console.error(`omnara: ${data.error}`)
    }
    process.exitCode = execExitCode(data)
  },
})

export const machineUploadOp = customOp({
  verb: 'upload',
  summary: 'Upload a local file to a path on a machine (10 MiB max)',
  path: schemas.zUploadMachineFilePath,
  configure: (command) => {
    command
      .argument('<local-file>', 'local file to upload')
      .argument('<remote-path>', 'absolute destination path on the machine, or ~/...')
      .option('--json', 'print the result as JSON')
  },
  run: async ({ client, path, args, options }) => {
    const [localFile, remotePath] = args
    const file = parseWithSchema(z.string().min(1), localFile, '<local-file>')
    const query = parseWithSchema(
      schemas.zUploadMachineFileQuery,
      { path: remotePath ?? null },
      '<remote-path>',
    )
    if ((await stat(file)).size > MAX_MEMORY_FILE_BYTES) {
      throw new CliInputError('file exceeds the 10 MiB upload limit')
    }
    const body = await loadMemoryUpload(file)
    const { data } = await sdk.uploadMachineFile({ client, path, query, body })
    renderResult(data, options.json === true)
  },
})

export const machineDownloadOp = customOp({
  verb: 'download',
  summary: 'Download a file from a machine (10 MiB max); use - to write to stdout',
  path: schemas.zDownloadMachineFilePath,
  configure: (command) => {
    command
      .argument('<remote-path>', 'absolute path on the machine, or ~/...')
      .argument(
        '[local-file]',
        'local destination, or - for stdout (default: the remote file name)',
      )
      .option('--json', 'print download metadata as JSON')
  },
  run: async ({ client, path, args, options }) => {
    const [remotePath, localArg] = args
    const query = parseWithSchema(
      schemas.zDownloadMachineFileQuery,
      { path: remotePath ?? null },
      '<remote-path>',
    )
    const output =
      localArg !== undefined && localArg !== ''
        ? localArg
        : basename(query.path.replaceAll('\\', '/'))
    const { response } = await sdk.downloadMachineFile({ client, path, query, parseAs: 'stream' })
    const digest = response.headers.get(FILE_DIGEST_HEADER)
    if (!digest) throw new CliInputError('file response is missing its digest')
    const bytes = new Uint8Array(await response.arrayBuffer())
    if (output === '-') {
      process.stdout.write(bytes)
      return
    }
    await writeFile(output, bytes)
    renderResult({ output, size_bytes: bytes.length, digest }, options.json === true)
  },
})
