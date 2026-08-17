import { readdirSync, readFileSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { gzipSync } from 'node:zlib'

import { zSkillOwnerInput } from '@omnara/sdk/zod'
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

interface TarEntry {
  name: string
  mode: number
  size: number
  typeflag: '0' | '5'
  content?: Buffer
}

function collectEntries(rootDir: string, rootName: string): TarEntry[] {
  const entries: TarEntry[] = [{ name: `${rootName}/`, mode: 0o755, size: 0, typeflag: '5' }]
  const walk = (dir: string, prefix: string): void => {
    const children = readdirSync(dir, { withFileTypes: true }).sort((a, b) =>
      a.name.localeCompare(b.name),
    )
    for (const child of children) {
      const fullPath = join(dir, child.name)
      const entryName = `${prefix}${child.name}`
      if (child.isSymbolicLink()) {
        throw new CliInputError(
          `cannot pack ${fullPath}: symlinks are not allowed in skill archives`,
        )
      }
      if (child.isDirectory()) {
        entries.push({ name: `${entryName}/`, mode: 0o755, size: 0, typeflag: '5' })
        walk(fullPath, `${entryName}/`)
      } else if (child.isFile()) {
        const content = readFileSync(fullPath)
        entries.push({ name: entryName, mode: 0o644, size: content.length, typeflag: '0', content })
      } else {
        throw new CliInputError(`cannot pack ${fullPath}: not a regular file`)
      }
    }
  }
  walk(rootDir, `${rootName}/`)
  return entries
}

function splitTarName(name: string): { prefix: string; base: string } {
  if (Buffer.byteLength(name) <= 100) return { prefix: '', base: name }
  for (let index = name.indexOf('/'); index !== -1; index = name.indexOf('/', index + 1)) {
    const prefix = name.slice(0, index)
    const base = name.slice(index + 1)
    if (Buffer.byteLength(prefix) <= 155 && Buffer.byteLength(base) <= 100) {
      return { prefix, base }
    }
  }
  throw new CliInputError(`path too long for skill archive: ${name}`)
}

function writeOctal(header: Buffer, offset: number, length: number, value: number): void {
  header.write(value.toString(8).padStart(length - 1, '0'), offset, 'ascii')
}

function tarHeaderFor(entry: TarEntry): Buffer {
  const header = Buffer.alloc(512)
  const { prefix, base } = splitTarName(entry.name)
  header.write(base, 0, 100, 'utf8')
  writeOctal(header, 100, 8, entry.mode)
  writeOctal(header, 108, 8, 0)
  writeOctal(header, 116, 8, 0)
  writeOctal(header, 124, 12, entry.size)
  writeOctal(header, 136, 12, 0)
  header.fill(0x20, 148, 156)
  header.write(entry.typeflag, 156, 1, 'ascii')
  header.write('ustar', 257, 5, 'ascii')
  header.write('00', 263, 2, 'ascii')
  header.write(prefix, 345, 155, 'utf8')
  let checksum = 0
  for (const byte of header) checksum += byte
  writeOctal(header, 148, 7, checksum)
  header[155] = 0x20
  return header
}

function packDirectory(dir: string): Buffer {
  const rootName = basename(resolve(dir))
  const blocks: Buffer[] = []
  for (const entry of collectEntries(dir, rootName)) {
    blocks.push(tarHeaderFor(entry))
    if (entry.content !== undefined && entry.content.length > 0) {
      blocks.push(entry.content)
      const remainder = entry.content.length % 512
      if (remainder !== 0) blocks.push(Buffer.alloc(512 - remainder))
    }
  }
  blocks.push(Buffer.alloc(1024))
  return gzipSync(Buffer.concat(blocks))
}

export function loadSkillArchive(archivePath: string): File {
  let isDirectory: boolean
  try {
    isDirectory = statSync(archivePath).isDirectory()
  } catch {
    throw new CliInputError(`archive path not found: ${archivePath}`)
  }
  if (isDirectory) {
    const buffer = packDirectory(archivePath)
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
