import { parseDocument, Scalar, visit, YAMLParseError } from 'yaml'
import * as z from 'zod'

export interface SkillSourceFile {
  path: string
  data: Blob
}

export interface SkillBundle {
  label: string
  sourcePath: string
  archive: File
  problem?: string
}

export class SkillArchiveError extends Error {}

const MISSING_NAME = 'SKILL.md frontmatter is missing `name`.'
const MISSING_DESCRIPTION = 'SKILL.md frontmatter is missing `description`.'
const MAX_SKILL_MD_BYTES = 256 * 1024
const MAX_DESCRIPTION_CHARS = 1024

const YAML_NULLS = new Set(['', '~', 'null', 'Null', 'NULL'])

const ZIP_OPTIONS = { useWebWorkers: false }

const zSkillFrontmatter = z.object(
  {
    name: z
      .string({ error: MISSING_NAME })
      .min(1, { error: MISSING_NAME })
      .max(64, { error: 'SKILL.md frontmatter `name` cannot exceed 64 characters.' })
      .regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, {
        error:
          'SKILL.md frontmatter `name` must use lowercase letters and digits separated by single hyphens.',
      }),
    description: z
      .string({ error: MISSING_DESCRIPTION })
      .trim()
      .min(1, { error: MISSING_DESCRIPTION })
      .refine((value) => Array.from(value).length <= MAX_DESCRIPTION_CHARS, {
        error: 'SKILL.md frontmatter `description` exceeds 1,024 characters.',
      }),
  },
  { error: 'SKILL.md frontmatter must be a YAML mapping.' },
)

function isMacOSMetadata(path: string) {
  const segments = path.split('/')
  const last = segments.at(-1) ?? ''
  return segments[0] === '__MACOSX' || last === '.DS_Store' || last.startsWith('._')
}

function parentDir(path: string) {
  const slash = path.lastIndexOf('/')
  return slash < 0 ? '' : path.slice(0, slash)
}

function baseName(path: string) {
  return path.slice(path.lastIndexOf('/') + 1)
}

function joinPath(dir: string, path: string) {
  if (dir === '') return path
  return path === '' ? dir : `${dir}/${path}`
}

function relativePath(path: string, dir: string) {
  return dir === '' ? path : path.slice(dir.length + 1)
}

function isSkillMdPath(path: string) {
  return baseName(path).toLowerCase() === 'skill.md'
}

function isWithin(path: string, dir: string) {
  return dir === '' || path.startsWith(`${dir}/`)
}

function isIgnoredDir(name: string) {
  return name.startsWith('.') || name === 'node_modules'
}

function isIgnoredPath(path: string) {
  return path.split('/').slice(0, -1).some(isIgnoredDir)
}

function archiveStem(filename: string) {
  return filename.replace(/\.(zip|tar\.gz|tgz)$/i, '')
}

export interface SkillMdProblem {
  message: string
  startLine: number
  endLine: number
}

export type SkillMdCheck = { ok: true; name: string } | { ok: false; problem: SkillMdProblem }

const FRONTMATTER_KEYS = ['name', 'description']

function quoteColonDescriptions(frontmatter: string) {
  return frontmatter.replace(/^([ \t]*description:)(.*)$/gm, (line, key: string, rest: string) => {
    const value = rest.trim()
    return value.includes(':') && !/^['"|>]/.test(value) ? `${key} ${JSON.stringify(value)}` : line
  })
}

function parseFrontmatterBlock(frontmatter: string) {
  const document = parseDocument(frontmatter, { schema: 'failsafe' })
  const [error] = document.errors
  if (error) throw error
  visit(document, {
    Scalar(key, node) {
      if (
        key === 'value' &&
        node.type === Scalar.PLAIN &&
        node.source !== undefined &&
        YAML_NULLS.has(node.source)
      ) {
        node.value = ''
      }
    },
  })
  return zSkillFrontmatter.safeParse(document.toJS())
}

function parseFrontmatter(frontmatter: string) {
  try {
    return parseFrontmatterBlock(frontmatter)
  } catch {
    return parseFrontmatterBlock(quoteColonDescriptions(frontmatter))
  }
}

export function checkSkillMd(skillMd: string, expectedName?: string): SkillMdCheck {
  if (new TextEncoder().encode(skillMd).length > MAX_SKILL_MD_BYTES) {
    return {
      ok: false,
      problem: { message: 'SKILL.md exceeds 256 KB.', startLine: 1, endLine: 1 },
    }
  }
  const lines = skillMd.replace(/^﻿/, '').split(/\r?\n/)
  const close = lines.findIndex((line, index) => index > 0 && /^---\r*$/.test(line))
  if (!lines[0]?.startsWith('---') || close < 0) {
    return {
      ok: false,
      problem: {
        message: "SKILL.md is missing YAML frontmatter delimited by '---'.",
        startLine: 1,
        endLine: 1,
      },
    }
  }
  const frontmatterBlock = { startLine: 1, endLine: close + 1 }
  let fields: ReturnType<typeof parseFrontmatter>
  try {
    fields = parseFrontmatter(lines.slice(1, close).join('\n'))
  } catch (error) {
    const line = error instanceof YAMLParseError ? error.linePos?.[0].line : undefined
    const location =
      line === undefined ? frontmatterBlock : { startLine: line + 1, endLine: line + 1 }
    return {
      ok: false,
      problem: { ...location, message: 'SKILL.md frontmatter is not valid YAML.' },
    }
  }
  function keyLocation(key: string | undefined) {
    const keyIndex = lines.findIndex(
      (line, index) =>
        index > 0 && index < close && key !== undefined && line.startsWith(`${key}:`),
    )
    return keyIndex < 0 ? frontmatterBlock : { startLine: keyIndex + 1, endLine: keyIndex + 1 }
  }
  if (fields.success) {
    const { name } = fields.data
    if (expectedName === undefined || name === expectedName) return { ok: true, name }
    return {
      ok: false,
      problem: {
        ...keyLocation('name'),
        message: `SKILL.md frontmatter \`name\` must stay \`${expectedName}\`.`,
      },
    }
  }
  const issue = fields.error.issues[0]
  const key = FRONTMATTER_KEYS.find((candidate) => issue?.path[0] === candidate)
  return { ok: false, problem: { ...keyLocation(key), message: issue?.message ?? MISSING_NAME } }
}

async function zipSkill(
  name: string,
  sourcePath: string,
  files: SkillSourceFile[],
  problem?: string,
): Promise<SkillBundle> {
  const { BlobReader, BlobWriter, ZipWriter } = await import('@zip.js/zip.js')
  const writer = new ZipWriter(new BlobWriter('application/zip'), ZIP_OPTIONS)
  await Promise.all(
    files.map((file) => writer.add(`${name}/${file.path}`, new BlobReader(file.data))),
  )
  const archive = new File([await writer.close()], `${name}.zip`, { type: 'application/zip' })
  return { label: name, sourcePath, archive, problem }
}

interface SkillRoot {
  dir: string
  files: SkillSourceFile[]
}

function containerDir(paths: string[]) {
  const top = paths[0]?.split('/')[0] ?? ''
  return paths.every((path) => path.startsWith(`${top}/`)) ? top : ''
}

function skillRoots(files: SkillSourceFile[], skipIgnored: boolean): SkillRoot[] {
  const sources = files.filter((file) => !isMacOSMetadata(file.path))
  const container = containerDir(sources.map((file) => file.path))
  const skillMdDirs = new Set(
    sources.flatMap((file) => {
      const path = relativePath(file.path, container)
      return isSkillMdPath(path) ? [parentDir(path)] : []
    }),
  )
  const dirs = skillMdDirs.has('')
    ? ['']
    : [...skillMdDirs]
        .filter((dir) => !dir.includes('/') && !(skipIgnored && isIgnoredDir(dir)))
        .sort()
  return dirs.map((dir) => {
    const root = joinPath(container, dir)
    return {
      dir: root,
      files: sources.flatMap((file) => {
        if (!isWithin(file.path, root)) return []
        const path = relativePath(file.path, root)
        return skipIgnored && isIgnoredPath(path) ? [] : [{ path, data: file.data }]
      }),
    }
  })
}

async function bundleRoot({ dir, files }: SkillRoot, fallbackName: string): Promise<SkillBundle> {
  const fallback = dir === '' ? fallbackName : baseName(dir)
  const skillMds = files.filter((file) => !file.path.includes('/') && isSkillMdPath(file.path))
  const [skillMd, ...extra] = skillMds
  if (extra.length > 0) {
    return zipSkill(fallback, dir, files, 'Skill contains more than one SKILL.md file.')
  }
  const check = checkSkillMd(skillMd ? await skillMd.data.text() : '')
  return check.ok
    ? zipSkill(check.name, dir, files)
    : zipSkill(fallback, dir, files, check.problem.message)
}

function bundleRoots(roots: SkillRoot[], fallbackName: string) {
  return Promise.all(roots.map((root) => bundleRoot(root, fallbackName)))
}

export function bundleSkills(
  files: SkillSourceFile[],
  fallbackName: string,
  { skipIgnored }: { skipIgnored: boolean },
): Promise<SkillBundle[]> {
  return bundleRoots(skillRoots(files, skipIgnored), fallbackName)
}

function bundleSkillMd(skillMd: string): Promise<SkillBundle> {
  return bundleRoot({ dir: '', files: [{ path: 'SKILL.md', data: new Blob([skillMd]) }] }, 'skill')
}

export type SkillSource =
  | { kind: 'folder'; files: File[] }
  | { kind: 'files'; files: File[] }
  | { kind: 'drop'; entries: FileSystemEntry[] }
  | { kind: 'skill-md'; text: string }

function isArchiveName(filename: string) {
  return /\.(zip|tar\.gz|tgz)$/i.test(filename)
}

export function skillSourceName(source: SkillSource) {
  if (source.kind === 'skill-md') return 'SKILL.md'
  if (source.kind === 'files') {
    const [only, ...rest] = source.files
    return only && rest.length === 0 ? only.name : `${source.files.length} files`
  }
  if (source.kind === 'folder') {
    return source.files[0]?.webkitRelativePath.split('/')[0] ?? 'folder'
  }
  const [only, ...rest] = source.entries
  return only && rest.length === 0 ? only.name : `${source.entries.length} items`
}

export type SkillSourceRead = { ok: true; bundles: SkillBundle[] } | { ok: false; message: string }

export async function readSkillSource(
  source: SkillSource,
  read: (source: SkillSource) => Promise<SkillBundle[]>,
): Promise<SkillSourceRead> {
  try {
    return { ok: true, bundles: await read(source) }
  } catch (error) {
    return {
      ok: false,
      message:
        error instanceof SkillArchiveError
          ? error.message
          : `Could not read ${skillSourceName(source)}. Choose a folder, .zip, or .tar.gz archive.`,
    }
  }
}

function readEntryBatch(reader: FileSystemDirectoryReader) {
  return new Promise<FileSystemEntry[]>((resolve, reject) => {
    reader.readEntries(resolve, reject)
  })
}

function readEntryFile(entry: FileSystemFileEntry) {
  return new Promise<File>((resolve, reject) => {
    entry.file(resolve, reject)
  })
}

async function directoryChildren(entry: FileSystemDirectoryEntry) {
  const reader = entry.createReader()
  const children: FileSystemEntry[] = []
  let batch = await readEntryBatch(reader)
  while (batch.length > 0) {
    children.push(...batch)
    batch = await readEntryBatch(reader)
  }
  return children
}

function isSkillMdEntry(entry: FileSystemEntry) {
  return entry instanceof FileSystemFileEntry && isSkillMdPath(entry.name)
}

async function childFiles(children: FileSystemEntry[], path: string): Promise<SkillSourceFile[]> {
  const nested = await Promise.all(
    children.map(async (child): Promise<SkillSourceFile[]> => {
      const childPath = `${path}/${child.name}`
      if (child instanceof FileSystemFileEntry) {
        return [{ path: childPath, data: await readEntryFile(child) }]
      }
      if (!(child instanceof FileSystemDirectoryEntry) || isIgnoredDir(child.name)) return []
      return childFiles(await directoryChildren(child), childPath)
    }),
  )
  return nested.flat()
}

async function droppedFolderFiles(folder: FileSystemDirectoryEntry) {
  const children = await directoryChildren(folder)
  if (children.some(isSkillMdEntry)) return childFiles(children, folder.name)
  const skills = await Promise.all(
    children.map(async (child) => {
      if (!(child instanceof FileSystemDirectoryEntry) || isIgnoredDir(child.name)) return []
      const grandchildren = await directoryChildren(child)
      return grandchildren.some(isSkillMdEntry)
        ? childFiles(grandchildren, `${folder.name}/${child.name}`)
        : []
    }),
  )
  return skills.flat()
}

function bundleLooseFiles(files: SkillSourceFile[]): Promise<SkillBundle[]> {
  const skillMds = files.filter((file) => isSkillMdPath(file.path))
  if (skillMds.length <= 1) return bundleSkills(files, 'skill', { skipIgnored: false })
  return Promise.all(
    skillMds.map(async (file, index) => ({
      ...(await bundleRoot({ dir: '', files: [{ path: 'SKILL.md', data: file.data }] }, 'skill')),
      sourcePath: `${file.path} #${index + 1}`,
    })),
  )
}

async function bundleMixed(
  looseFiles: SkillSourceFile[],
  folders: SkillSourceFile[][],
  archives: File[],
) {
  const bundled = await Promise.all([
    bundleLooseFiles(looseFiles),
    ...folders.map((files) => bundleSkills(files, 'skill', { skipIgnored: true })),
    ...archives.map(bundleArchive),
  ])
  return bundled.flat()
}

async function bundleDrop(entries: FileSystemEntry[]): Promise<SkillBundle[]> {
  const fileEntries = entries.filter((entry) => entry instanceof FileSystemFileEntry)
  const folderEntries = entries.filter((entry) => entry instanceof FileSystemDirectoryEntry)
  const [files, folders] = await Promise.all([
    Promise.all(fileEntries.map(readEntryFile)),
    Promise.all(folderEntries.map(droppedFolderFiles)),
  ])
  return bundleMixed(
    files.flatMap((file) => (isArchiveName(file.name) ? [] : [{ path: file.name, data: file }])),
    folders,
    files.filter((file) => isArchiveName(file.name)),
  )
}

function bundleFiles(files: File[]): Promise<SkillBundle[]> {
  return bundleMixed(
    files.flatMap((file) => (isArchiveName(file.name) ? [] : [{ path: file.name, data: file }])),
    [],
    files.filter((file) => isArchiveName(file.name)),
  )
}

function bundleFolder(files: File[]): Promise<SkillBundle[]> {
  return bundleMixed(
    [],
    [files.map((file) => ({ path: file.webkitRelativePath || file.name, data: file }))],
    [],
  )
}

function symlinkError(path: string) {
  return new SkillArchiveError(`Skill archive contains a symlink at ${path}.`)
}

async function readZip(file: File): Promise<SkillSourceFile[]> {
  const { BlobReader, BlobWriter, ZipReader } = await import('@zip.js/zip.js')
  const reader = new ZipReader(new BlobReader(file), ZIP_OPTIONS)
  try {
    const entries = await reader.getEntries()
    const symlink = entries.find((entry) => entry.symlink)
    if (symlink) throw symlinkError(symlink.filename)
    return await Promise.all(
      entries.flatMap((entry) =>
        entry.directory
          ? []
          : [entry.getData(new BlobWriter()).then((data) => ({ path: entry.filename, data }))],
      ),
    )
  } finally {
    await reader.close()
  }
}

async function readTarGz(file: File): Promise<SkillSourceFile[]> {
  const { unpackTar } = await import('modern-tar')
  const entries = await unpackTar(file.stream().pipeThrough(new DecompressionStream('gzip')))
  return entries.flatMap(({ header, data }) => {
    if (header.type === 'symlink' || header.type === 'link') throw symlinkError(header.name)
    if (header.type === 'directory') return []
    if (header.type !== 'file') {
      throw new SkillArchiveError(`Skill archive has an unsupported entry at ${header.name}.`)
    }
    return [{ path: header.name, data: new Blob(data ? [data] : []) }]
  })
}

export async function bundleArchive(file: File): Promise<SkillBundle[]> {
  const sources = await (/\.zip$/i.test(file.name) ? readZip(file) : readTarGz(file))
  const bundles = await bundleSkills(
    sources.map((source) => ({ ...source, path: source.path.replace(/^\.\//, '') })),
    archiveStem(file.name),
    { skipIgnored: false },
  )
  return bundles.map((bundle) => ({
    ...bundle,
    sourcePath: bundle.sourcePath === '' ? file.name : `${file.name}/${bundle.sourcePath}`,
  }))
}

export async function bundleSource(source: SkillSource): Promise<SkillBundle[]> {
  if (source.kind === 'skill-md') return [await bundleSkillMd(source.text)]
  if (source.kind === 'files') return bundleFiles(source.files)
  if (source.kind === 'folder') return bundleFolder(source.files)
  return bundleDrop(source.entries)
}
