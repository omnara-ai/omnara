import { open, writeFile } from 'node:fs/promises'
import { buffer } from 'node:stream/consumers'

import { MAX_MEMORY_FILE_BYTES, sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { customOp, parseWithSchema } from './factory.ts'
import { CliInputError, renderResult } from './output.ts'

export const zMemoryUploadBody = z.object({
  file: z.string().min(1).describe('local file to upload'),
})

export async function loadMemoryUpload(file: string): Promise<Blob> {
  const handle = await open(file, 'r')
  try {
    if ((await handle.stat()).size > MAX_MEMORY_FILE_BYTES) {
      throw new CliInputError('memory file exceeds the 10 MiB upload limit')
    }
    const bytes = await buffer(
      handle.createReadStream({ end: MAX_MEMORY_FILE_BYTES, autoClose: false }),
    )
    if (bytes.length > MAX_MEMORY_FILE_BYTES) {
      throw new CliInputError('memory file exceeds the 10 MiB upload limit')
    }
    return new Blob([new Uint8Array(bytes)])
  } finally {
    await handle.close()
  }
}

export const memoryDownloadOp = customOp({
  verb: 'download',
  summary: 'Download a memory file',
  path: schemas.zDownloadMemoryFilePath,
  configure: (command) => {
    command
      .requiredOption('--path <path>', 'file path relative to the store root')
      .requiredOption('--output <file>', 'local destination; must not already exist')
      .option('--json', 'print download metadata as JSON')
  },
  run: async ({ client, path, options }) => {
    const query = parseWithSchema(
      schemas.zDownloadMemoryFileQuery,
      { path: options.path ?? null },
      'query flags',
    )
    const output = parseWithSchema(z.string().min(1), options.output, '--output')
    const { response } = await sdk.downloadMemoryFile({ client, path, query, parseAs: 'stream' })
    const digest = response.headers.get('X-Omnara-File-Digest')
    if (!digest) throw new CliInputError('file response is missing its digest')
    const bytes = new Uint8Array(await response.arrayBuffer())
    await writeFile(output, bytes, { flag: 'wx' })
    renderResult({ output, digest }, options.json === true)
  },
})
