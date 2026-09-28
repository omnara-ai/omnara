import { gunzipSync, strToU8, unzipSync, zipSync } from 'fflate'
import { parse, YAMLParseError } from 'yaml'
import * as z from 'zod'

export interface SkillSourceFile {
  path: string
  data: Uint8Array
}

export interface SkillBundle {
  label: string
  sourcePath: string
  archive: File
  problem?: string
}

const MISSING_NAME = 'SKILL.md frontmatter is missing `name`.'
const MISSING_DESCRIPTION = 'SKILL.md frontmatter is missing `description`.'

const YAML_NULLS = new Set(['~', 'null', 'Null', 'NULL'])

function yamlText(missing: string) {
  return z.string({ error: missing }).transform((value) => (YAML_NULLS.has(value) ? '' : value))
}

const zSkillFrontmatter = z.object(
  {
    name: yamlText(MISSING_NAME).pipe(
      z
        .string()
        .min(1, { error: MISSING_NAME })
        .max(64, { error: 'SKILL.md frontmatter `name` cannot exceed 64 characters.' })
        .regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, {
          error:
            'SKILL.md frontmatter `name` must use lowercase letters and digits separated by single hyphens.',
        }),
    ),
    description: yamlText(MISSING_DESCRIPTION).pipe(
      z
        .string()
        .trim()
        .min(1, { error: MISSING_DESCRIPTION })
        .max(1024, { error: 'SKILL.md frontmatter `description` exceeds 1,024 characters.' }),
    ),
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

function isSkillMdPath(path: string) {
  return baseName(path).toLowerCase() === 'skill.md'
}

function isWithin(path: string, dir: string) {
  return dir === '' || path.startsWith(`${dir}/`)
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

function quoteColonDescription(frontmatter: string) {
  return frontmatter.replace(
    /^(\s*description:[ \t]*)([^'"|>\s].*:.*)$/m,
    (_, key, value) => `${key}${JSON.stringify(value)}`,
  )
}

function parseFrontmatter(frontmatter: string) {
  try {
    return zSkillFrontmatter.safeParse(parse(frontmatter, { schema: 'failsafe' }))
  } catch {
    return zSkillFrontmatter.safeParse(
      parse(quoteColonDescription(frontmatter), { schema: 'failsafe' }),
    )
  }
}

export function checkSkillMd(skillMd: string, expectedName?: string): SkillMdCheck {
  const lines = skillMd.replace(/^\uFEFF/, '').split(/\r?\n/)
  const close = lines.findIndex((line, index) => index > 0 && line.trimEnd() === '---')
  if (lines[0]?.trimEnd() !== '---' || close < 0) {
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

function zipSkill(
  name: string,
  sourcePath: string,
  files: SkillSourceFile[],
  problem?: string,
): SkillBundle {
  const entries = Object.fromEntries(files.map((file) => [`${name}/${file.path}`, file.data]))
  const archive = new File([zipSync(entries)], `${name}.zip`, { type: 'application/zip' })
  return { label: name, sourcePath, archive, problem }
}

export function bundleSkillMd(skillMd: string): SkillBundle {
  const check = checkSkillMd(skillMd)
  return zipSkill(
    check.ok ? check.name : 'skill',
    'SKILL.md',
    [{ path: 'SKILL.md', data: strToU8(skillMd) }],
    check.ok ? undefined : check.problem.message,
  )
}

export function bundleSkills(files: SkillSourceFile[], fallbackName: string): SkillBundle[] {
  const sources = files.filter((file) => !isMacOSMetadata(file.path))
  const skillMds = sources.filter((file) => isSkillMdPath(file.path))
  const skillDirs = [...new Set(skillMds.map((file) => parentDir(file.path)))]
  const roots = skillDirs
    .filter((dir) => !skillDirs.some((other) => other !== dir && isWithin(dir, other)))
    .sort()
  return roots.map((root) => {
    const skillMd = skillMds.find((file) => parentDir(file.path) === root)
    const check = checkSkillMd(skillMd ? new TextDecoder().decode(skillMd.data) : '')
    const fallback = root === '' ? fallbackName : baseName(root)
    const prefix = root === '' ? 0 : root.length + 1
    const rootFiles = sources
      .filter((file) => isWithin(file.path, root))
      .map((file) => ({ path: file.path.slice(prefix), data: file.data }))
    return check.ok
      ? zipSkill(check.name, root, rootFiles)
      : zipSkill(fallback, root, rootFiles, check.problem.message)
  })
}

export type SkillSource =
  | { kind: 'folder'; files: File[] }
  | { kind: 'files'; files: File[] }
  | { kind: 'drop'; entries: FileSystemEntry[] }

function isArchiveName(filename: string) {
  return /\.(zip|tar\.gz|tgz)$/i.test(filename)
}

export function skillSourceName(source: SkillSource) {
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

async function entryFiles(entry: FileSystemEntry, path: string): Promise<SkillSourceFile[]> {
  if (entry instanceof FileSystemFileEntry) {
    const file = await readEntryFile(entry)
    return [{ path, data: new Uint8Array(await file.arrayBuffer()) }]
  }
  if (!(entry instanceof FileSystemDirectoryEntry)) return []
  const reader = entry.createReader()
  const children: FileSystemEntry[] = []
  let batch = await readEntryBatch(reader)
  while (batch.length > 0) {
    children.push(...batch)
    batch = await readEntryBatch(reader)
  }
  const nested = await Promise.all(
    children.map((child) => entryFiles(child, `${path}/${child.name}`)),
  )
  return nested.flat()
}

function bundleLooseFiles(files: SkillSourceFile[]): SkillBundle[] {
  const skillMds = files.filter((file) => isSkillMdPath(file.path))
  if (skillMds.length <= 1) return bundleSkills(files, 'skill')
  return skillMds.flatMap((file, index) =>
    bundleSkills([{ path: 'SKILL.md', data: file.data }], 'skill').map((bundle) => ({
      ...bundle,
      sourcePath: `${file.path} #${index + 1}`,
    })),
  )
}

async function bundleMixed(
  looseFiles: SkillSourceFile[],
  folderFiles: SkillSourceFile[],
  archives: File[],
) {
  const archiveBundles = await Promise.all(archives.map(bundleArchive))
  return [
    ...bundleLooseFiles(looseFiles),
    ...bundleSkills(folderFiles, 'skill'),
    ...archiveBundles.flat(),
  ]
}

async function bundleDrop(entries: FileSystemEntry[]): Promise<SkillBundle[]> {
  const archiveEntries = entries.flatMap((entry) =>
    entry instanceof FileSystemFileEntry && isArchiveName(entry.name) ? [entry] : [],
  )
  const looseEntries = entries.flatMap((entry) =>
    entry instanceof FileSystemFileEntry && !isArchiveName(entry.name) ? [entry] : [],
  )
  const folderEntries = entries.filter((entry) => !(entry instanceof FileSystemFileEntry))
  const [archives, loose, folders] = await Promise.all([
    Promise.all(archiveEntries.map(readEntryFile)),
    Promise.all(looseEntries.map((entry) => entryFiles(entry, entry.name))),
    Promise.all(folderEntries.map((entry) => entryFiles(entry, entry.name))),
  ])
  return bundleMixed(loose.flat(), folders.flat(), archives)
}

async function bundleFiles(files: File[]): Promise<SkillBundle[]> {
  const archives = files.filter((file) => isArchiveName(file.name))
  const loose = await Promise.all(
    files
      .filter((file) => !isArchiveName(file.name))
      .map(async (file) => ({ path: file.name, data: new Uint8Array(await file.arrayBuffer()) })),
  )
  return bundleMixed(loose, [], archives)
}

async function bundleFolder(files: File[]): Promise<SkillBundle[]> {
  const sources = await Promise.all(
    files.map(async (file) => ({
      path: file.webkitRelativePath || file.name,
      data: new Uint8Array(await file.arrayBuffer()),
    })),
  )
  return bundleSkills(sources, 'skill')
}

const TAR_BLOCK = 512

function tarString(bytes: Uint8Array, start: number, length: number) {
  const field = bytes.subarray(start, start + length)
  const end = field.indexOf(0)
  return new TextDecoder().decode(end < 0 ? field : field.subarray(0, end))
}

function paxPath(records: string) {
  for (const record of records.split('\n')) {
    const match = /^\d+ path=(.*)$/.exec(record)
    if (match?.[1] !== undefined) return match[1]
  }
  return undefined
}

function readTar(bytes: Uint8Array): SkillSourceFile[] {
  const files: SkillSourceFile[] = []
  let longName: string | undefined
  for (let offset = 0; offset + TAR_BLOCK <= bytes.length; ) {
    const header = bytes.subarray(offset, offset + TAR_BLOCK)
    if (header.every((byte) => byte === 0)) break
    const size = Number.parseInt(tarString(header, 124, 12).trim() || '0', 8)
    const type = tarString(header, 156, 1) || '0'
    const prefix = tarString(header, 345, 155)
    const name = tarString(header, 0, 100)
    const data = bytes.subarray(offset + TAR_BLOCK, offset + TAR_BLOCK + size)
    offset += TAR_BLOCK + Math.ceil(size / TAR_BLOCK) * TAR_BLOCK
    if (type === 'x') {
      longName = paxPath(new TextDecoder().decode(data))
      continue
    }
    if (type === 'L') {
      longName = tarString(data, 0, data.length)
      continue
    }
    const path = longName ?? (prefix ? `${prefix}/${name}` : name)
    longName = undefined
    if (type === '1' || type === '2') {
      throw new Error(`skill archive contains a symlink at ${path}`)
    }
    if (type === '0' || type === '7') files.push({ path, data })
  }
  return files
}

function archiveSources(file: File, bytes: Uint8Array): SkillSourceFile[] {
  if (/\.zip$/i.test(file.name)) {
    return Object.entries(unzipSync(bytes))
      .filter(([path]) => !path.endsWith('/'))
      .map(([path, data]) => ({ path, data }))
  }
  return readTar(gunzipSync(bytes))
}

export async function bundleArchive(file: File): Promise<SkillBundle[]> {
  const sources = archiveSources(file, new Uint8Array(await file.arrayBuffer())).map((source) => ({
    ...source,
    path: source.path.replace(/^\.\//, ''),
  }))
  return bundleSkills(sources, archiveStem(file.name)).map((bundle) => ({
    ...bundle,
    sourcePath: bundle.sourcePath === '' ? file.name : `${file.name}/${bundle.sourcePath}`,
  }))
}

export async function bundleSource(source: SkillSource): Promise<SkillBundle[]> {
  if (source.kind === 'files') return bundleFiles(source.files)
  if (source.kind === 'folder') return bundleFolder(source.files)
  return bundleDrop(source.entries)
}
