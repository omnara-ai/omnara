import { readdirSync, readFileSync, statSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'

import { zSkillOwnerInput } from '@omnara/sdk/zod'
import * as tar from 'tar'
import * as z from 'zod'

import { CliInputError } from './output.ts'

export const zCreateSkillCliBody = z.object({
  owner: zSkillOwnerInput,
  archive: z
    .string()
    .min(1)
    .describe('path to a skill directory (packed automatically) or a .zip/.tar.gz archive'),
})

const ARCHIVE_SUFFIXES = ['.zip', '.tar.gz', '.tgz']

function assertPackable(dir: string): void {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const fullPath = join(dir, entry.name)
    if (entry.isSymbolicLink()) {
      throw new CliInputError(`cannot pack ${fullPath}: symlinks are not allowed in skill archives`)
    }
    if (entry.isDirectory()) assertPackable(fullPath)
    else if (!entry.isFile()) throw new CliInputError(`cannot pack ${fullPath}: not a regular file`)
  }
}

async function packDirectory(dir: string): Promise<Buffer> {
  const root = resolve(dir)
  assertPackable(root)
  const stream = tar.create({ cwd: dirname(root), gzip: true, portable: true }, [basename(root)])
  const chunks: Buffer[] = []
  for await (const chunk of stream) chunks.push(chunk)
  return Buffer.concat(chunks)
}

export async function loadSkillArchive(archivePath: string): Promise<File> {
  let isDirectory: boolean
  try {
    isDirectory = statSync(archivePath).isDirectory()
  } catch {
    throw new CliInputError(`archive path not found: ${archivePath}`)
  }
  if (isDirectory) {
    const buffer = await packDirectory(archivePath)
    return new File([new Uint8Array(buffer)], `${basename(resolve(archivePath))}.tar.gz`)
  }
  const filename = basename(archivePath)
  if (!ARCHIVE_SUFFIXES.some((suffix) => filename.toLowerCase().endsWith(suffix))) {
    throw new CliInputError(
      `archive must be a directory or a ${ARCHIVE_SUFFIXES.join('/')} file: ${archivePath}`,
    )
  }
  return new File([new Uint8Array(readFileSync(archivePath))], filename)
}
