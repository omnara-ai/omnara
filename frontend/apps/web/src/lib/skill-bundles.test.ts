import {
  BlobReader,
  BlobWriter,
  TextReader,
  TextWriter,
  ZipReader,
  ZipWriter,
} from '@zip.js/zip.js'
import { packTar, type TarEntry } from 'modern-tar'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as z from 'zod'

import sharedCases from '../../../../../testdata/skill-frontmatter-v1.json?raw'
import {
  bundleArchive,
  bundleSkills,
  bundleSource,
  checkSkillMd,
  type SkillBundle,
  skillSourceName,
} from './skill-bundles'

const ZIP_OPTIONS = { useWebWorkers: false }

function skillMd(name: string) {
  return `---\nname: ${name}\ndescription: Does ${name} things.\n---\n\n# ${name}\n`
}

function source(path: string, content: string) {
  return { path, data: new Blob([content]) }
}

async function archiveContents(bundle: SkillBundle | undefined) {
  if (!bundle) throw new Error('Missing bundle')
  const reader = new ZipReader(new BlobReader(bundle.archive), ZIP_OPTIONS)
  const entries = await reader.getEntries()
  const contents = await Promise.all(
    entries.flatMap((entry) =>
      entry.directory
        ? []
        : [entry.getData(new TextWriter()).then((text) => [entry.filename, text] as const)],
    ),
  )
  await reader.close()
  return Object.fromEntries(contents)
}

async function zipFile(
  name: string,
  entries: Record<string, string>,
  symlinks: Record<string, string> = {},
) {
  const writer = new ZipWriter(new BlobWriter('application/zip'), ZIP_OPTIONS)
  for (const [path, content] of Object.entries(entries)) {
    await writer.add(path, new TextReader(content))
  }
  for (const [path, target] of Object.entries(symlinks)) {
    await writer.add(path, new TextReader(target), { unixMode: 0o120777 })
  }
  return new File([await writer.close()], name)
}

async function tarGzFile(name: string, entries: TarEntry[]) {
  const tar = new Blob([await packTar(entries)])
  const gzip = await new Response(tar.stream().pipeThrough(new CompressionStream('gzip'))).blob()
  return new File([gzip], name)
}

function tarFile(path: string, content: string): TarEntry {
  return { header: { name: path, size: new TextEncoder().encode(content).length }, body: content }
}

function folderFile(path: string, content: string) {
  const file = new File([content], path.slice(path.lastIndexOf('/') + 1))
  Object.defineProperty(file, 'webkitRelativePath', { value: path })
  return file
}

const zSharedCases = z.array(z.object({ skill_md: z.string(), name: z.string().nullable() }))

describe('checkSkillMd', () => {
  it('agrees with the server on every shared frontmatter case', () => {
    for (const { skill_md: text, name } of zSharedCases.parse(JSON.parse(sharedCases))) {
      const check = checkSkillMd(text)
      expect(check.ok ? check.name : null, text).toBe(name)
    }
  })

  it('reads the frontmatter name', () => {
    expect(checkSkillMd(skillMd('pdf-tools'))).toEqual({ ok: true, name: 'pdf-tools' })
    expect(checkSkillMd(`\uFEFF---\r\nname: bom\r\ndescription: d\r\n---\r\n`)).toEqual({
      ok: true,
      name: 'bom',
    })
  })

  it('accepts an unquoted description containing a colon', () => {
    expect(checkSkillMd('---\nname: colon\ndescription: Use when: needed\n---\n')).toEqual({
      ok: true,
      name: 'colon',
    })
  })

  it('reads YAML scalars as text the way the server does', () => {
    expect(checkSkillMd('---\nname: 2024\ndescription: true\n---\n')).toEqual({
      ok: true,
      name: '2024',
    })
    expect(checkSkillMd('---\nname: ~\ndescription: d\n---\n')).toEqual({
      ok: false,
      problem: { message: 'SKILL.md frontmatter is missing `name`.', startLine: 2, endLine: 2 },
    })
  })

  it('flags a renamed skill when the name must stay the same', () => {
    expect(checkSkillMd(skillMd('pdf-tools'), 'pdf-tools')).toEqual({ ok: true, name: 'pdf-tools' })
    expect(checkSkillMd(skillMd('renamed'), 'pdf-tools')).toEqual({
      ok: false,
      problem: {
        message: 'SKILL.md frontmatter `name` must stay `pdf-tools`.',
        startLine: 2,
        endLine: 2,
      },
    })
  })

  it('enforces the server name and description rules', () => {
    expect(checkSkillMd('---\nname: Release_Notes\ndescription: d\n---\n')).toEqual({
      ok: false,
      problem: {
        message:
          'SKILL.md frontmatter `name` must use lowercase letters and digits separated by single hyphens.',
        startLine: 2,
        endLine: 2,
      },
    })
    expect(checkSkillMd(`---\nname: ${'a'.repeat(65)}\ndescription: d\n---\n`)).toEqual({
      ok: false,
      problem: {
        message: 'SKILL.md frontmatter `name` cannot exceed 64 characters.',
        startLine: 2,
        endLine: 2,
      },
    })
    expect(checkSkillMd(`---\nname: long\ndescription: ${'d'.repeat(1025)}\n---\n`)).toEqual({
      ok: false,
      problem: {
        message: 'SKILL.md frontmatter `description` exceeds 1,024 characters.',
        startLine: 3,
        endLine: 3,
      },
    })
    expect(checkSkillMd(`---\nname: cjk\ndescription: ${'説'.repeat(1024)}\n---\n`)).toEqual({
      ok: true,
      name: 'cjk',
    })
    expect(checkSkillMd(`---\nname: big\ndescription: d\n---\n${'x'.repeat(256 * 1024)}`)).toEqual({
      ok: false,
      problem: { message: 'SKILL.md exceeds 256 KB.', startLine: 1, endLine: 1 },
    })
  })

  it('treats only plain YAML nulls as missing', () => {
    expect(checkSkillMd('---\nname: quoted\ndescription: "null"\n---\n')).toEqual({
      ok: true,
      name: 'quoted',
    })
    expect(checkSkillMd("---\nname: quoted\ndescription: '~'\n---\n")).toEqual({
      ok: true,
      name: 'quoted',
    })
    expect(checkSkillMd('---\nname: plain\ndescription: null\n---\n')).toEqual({
      ok: false,
      problem: {
        message: 'SKILL.md frontmatter is missing `description`.',
        startLine: 3,
        endLine: 3,
      },
    })
  })

  it('matches the server frontmatter delimiters', () => {
    expect(checkSkillMd('---\nname: spaced\ndescription: d\n---  \n')).toEqual({
      ok: false,
      problem: {
        message: "SKILL.md is missing YAML frontmatter delimited by '---'.",
        startLine: 1,
        endLine: 1,
      },
    })
    expect(checkSkillMd('--- \nname: open\ndescription: d\n---\r\r\n')).toEqual({
      ok: true,
      name: 'open',
    })
  })

  it('reports missing or unusable frontmatter with the lines to highlight', () => {
    const missing = "SKILL.md is missing YAML frontmatter delimited by '---'."
    expect(checkSkillMd('# no frontmatter')).toEqual({
      ok: false,
      problem: { message: missing, startLine: 1, endLine: 1 },
    })
    expect(checkSkillMd('---\nname: unterminated\n')).toEqual({
      ok: false,
      problem: { message: missing, startLine: 1, endLine: 1 },
    })
    expect(checkSkillMd('---\nname: ok\ndescription: [unclosed\n---\n')).toEqual({
      ok: false,
      problem: { message: 'SKILL.md frontmatter is not valid YAML.', startLine: 3, endLine: 3 },
    })
    expect(checkSkillMd('---\n- item\n---\n')).toEqual({
      ok: false,
      problem: {
        message: 'SKILL.md frontmatter must be a YAML mapping.',
        startLine: 1,
        endLine: 3,
      },
    })
    expect(checkSkillMd('---\ndescription: nameless\n---\n')).toEqual({
      ok: false,
      problem: { message: 'SKILL.md frontmatter is missing `name`.', startLine: 1, endLine: 3 },
    })
    expect(checkSkillMd('---\nname: vague\ndescription: "  "\n---\n')).toEqual({
      ok: false,
      problem: {
        message: 'SKILL.md frontmatter is missing `description`.',
        startLine: 3,
        endLine: 3,
      },
    })
  })
})

describe('bundleSkills', () => {
  const keepAll = { skipIgnored: false }

  it('zips one skill under a directory named after its frontmatter', async () => {
    const [bundle, ...rest] = await bundleSkills(
      [
        source('My Skill/SKILL.md', skillMd('my-skill')),
        source('My Skill/scripts/run.py', 'print(1)'),
      ],
      'fallback',
      keepAll,
    )
    expect(rest).toEqual([])
    expect(bundle?.label).toBe('my-skill')
    expect(bundle?.archive.name).toBe('my-skill.zip')
    expect(await archiveContents(bundle)).toEqual({
      'my-skill/SKILL.md': skillMd('my-skill'),
      'my-skill/scripts/run.py': 'print(1)',
    })
  })

  it('splits a collection into one bundle per child directory with a SKILL.md', async () => {
    const bundles = await bundleSkills(
      [
        source('skills/beta/SKILL.md', skillMd('beta')),
        source('skills/alpha/SKILL.md', skillMd('alpha')),
        source('skills/alpha/templates/SKILL.md', 'nested template'),
        source('skills/README.md', 'collection readme'),
        source('skills/alpha/.DS_Store', ''),
        source('skills/group/nested/SKILL.md', skillMd('nested')),
      ],
      'fallback',
      keepAll,
    )
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['alpha', 'skills/alpha'],
      ['beta', 'skills/beta'],
    ])
    expect(Object.keys(await archiveContents(bundles[0])).sort()).toEqual([
      'alpha/SKILL.md',
      'alpha/templates/SKILL.md',
    ])
  })

  it('treats a SKILL.md at the top of the container as a single skill', async () => {
    const bundles = await bundleSkills(
      [source('solo/SKILL.md', skillMd('solo')), source('solo/child/SKILL.md', skillMd('child'))],
      'fallback',
      keepAll,
    )
    expect(bundles.map((bundle) => bundle.label)).toEqual(['solo'])
    expect(Object.keys(await archiveContents(bundles[0])).sort()).toEqual([
      'solo/SKILL.md',
      'solo/child/SKILL.md',
    ])
  })

  it('names a top-level SKILL.md skill from frontmatter, then flags the fallback', async () => {
    const [named] = await bundleSkills(
      [source('SKILL.md', skillMd('root-skill'))],
      'fallback',
      keepAll,
    )
    expect(Object.keys(await archiveContents(named))).toEqual(['root-skill/SKILL.md'])
    const [unnamed] = await bundleSkills(
      [source('skill.md', '# no frontmatter')],
      'fallback',
      keepAll,
    )
    expect(Object.keys(await archiveContents(unnamed))).toEqual(['fallback/skill.md'])
    expect(unnamed?.problem).toBe("SKILL.md is missing YAML frontmatter delimited by '---'.")
  })

  it('flags a skill with more than one SKILL.md', async () => {
    const [bundle] = await bundleSkills(
      [source('twin/SKILL.md', skillMd('twin')), source('twin/skill.md', skillMd('twin'))],
      'fallback',
      keepAll,
    )
    expect(bundle?.problem).toBe('Skill contains more than one SKILL.md file.')
  })

  it('returns nothing when no SKILL.md is present', async () => {
    expect(await bundleSkills([source('notes/README.md', 'hi')], 'fallback', keepAll)).toEqual([])
  })
})

describe('SKILL.md sources', () => {
  it('zips pasted SKILL.md content under its frontmatter name', async () => {
    const source = { kind: 'skill-md', text: skillMd('pasted') } as const
    expect(skillSourceName(source)).toBe('SKILL.md')
    const [bundle, ...rest] = await bundleSource(source)
    expect(rest).toEqual([])
    expect(bundle?.label).toBe('pasted')
    expect(bundle?.problem).toBeUndefined()
    expect(await archiveContents(bundle)).toEqual({ 'pasted/SKILL.md': skillMd('pasted') })
  })
})

describe('folder sources', () => {
  it('uses each file path relative to the picked folder', async () => {
    const files = [
      folderFile('picked/one/SKILL.md', skillMd('one')),
      folderFile('picked/two/SKILL.md', skillMd('two')),
      folderFile('picked/two/ref.md', 'reference'),
    ]
    expect(skillSourceName({ kind: 'folder', files })).toBe('picked')
    const bundles = await bundleSource({ kind: 'folder', files })
    expect(bundles.map((bundle) => bundle.label)).toEqual(['one', 'two'])
    expect(await archiveContents(bundles[1])).toEqual({
      'two/SKILL.md': skillMd('two'),
      'two/ref.md': 'reference',
    })
  })

  it('skips dot directories and node_modules, and finds no skills below the second level', async () => {
    const files = [
      folderFile('repo/one/SKILL.md', skillMd('one')),
      folderFile('repo/one/.env.example', 'KEY='),
      folderFile('repo/one/.git/HEAD', 'ref'),
      folderFile('repo/one/node_modules/pkg/index.js', 'x'),
      folderFile('repo/.hidden/SKILL.md', skillMd('hidden')),
      folderFile('repo/node_modules/vendor/SKILL.md', skillMd('vendor')),
      folderFile('repo/.claude/skills/deep/SKILL.md', skillMd('deep')),
    ]
    const [one, ...rest] = await bundleSource({ kind: 'folder', files })
    expect(rest).toEqual([])
    expect(await archiveContents(one)).toEqual({
      'one/SKILL.md': skillMd('one'),
      'one/.env.example': 'KEY=',
    })
  })
})

describe('file sources', () => {
  it('keeps several loose SKILL.md files as separate skills', async () => {
    const files = [
      new File([skillMd('first')], 'SKILL.md'),
      new File([skillMd('second')], 'SKILL.md'),
    ]
    const bundles = await bundleSource({ kind: 'files', files })
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['first', 'SKILL.md #1'],
      ['second', 'SKILL.md #2'],
    ])
    expect(await archiveContents(bundles[1])).toEqual({ 'second/SKILL.md': skillMd('second') })
  })

  it('bundles chosen SKILL.md files and archives together', async () => {
    const files = [
      new File([skillMd('loose')], 'SKILL.md'),
      await zipFile('packed.zip', { 'packed/SKILL.md': skillMd('packed') }),
    ]
    expect(skillSourceName({ kind: 'files', files })).toBe('2 files')
    const bundles = await bundleSource({ kind: 'files', files })
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['loose', ''],
      ['packed', 'packed.zip/packed'],
    ])
  })
})

describe('bundleArchive', () => {
  it('rebundles every skill found in a zip', async () => {
    const zip = await zipFile('bundle.zip', {
      'bundle/a/SKILL.md': skillMd('a'),
      'bundle/b/SKILL.md': skillMd('b'),
    })
    const bundles = await bundleArchive(zip)
    expect(bundles.map((bundle) => bundle.label)).toEqual(['a', 'b'])
  })

  it('keeps dot directories and node_modules inside an archive', async () => {
    const zip = await zipFile('release-notes.zip', {
      'release-notes/SKILL.md': skillMd('release-notes'),
      'release-notes/.templates/example.md': 'example',
      'release-notes/node_modules/pkg/index.js': 'x',
    })
    const [bundle] = await bundleArchive(zip)
    expect(await archiveContents(bundle)).toEqual({
      'release-notes/SKILL.md': skillMd('release-notes'),
      'release-notes/.templates/example.md': 'example',
      'release-notes/node_modules/pkg/index.js': 'x',
    })
  })

  it('reads every skill in a tar.gz and rebundles it under the frontmatter name', async () => {
    const file = await tarGzFile('set.tar.gz', [
      { header: { name: './set/', type: 'directory', size: 0 } },
      tarFile('./set/Release Notes/SKILL.md', skillMd('release-notes')),
      tarFile('./set/Release Notes/template.md', 'template'),
      tarFile('./set/slides/SKILL.md', skillMd('slides')),
    ])
    const bundles = await bundleArchive(file)
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['release-notes', 'set.tar.gz/set/Release Notes'],
      ['slides', 'set.tar.gz/set/slides'],
    ])
    expect(await archiveContents(bundles[0])).toEqual({
      'release-notes/SKILL.md': skillMd('release-notes'),
      'release-notes/template.md': 'template',
    })
  })

  it('reads long tar paths', async () => {
    const dir = `set/${'d'.repeat(120)}`
    const file = await tarGzFile('long.tgz', [tarFile(`${dir}/SKILL.md`, skillMd('long'))])
    const bundles = await bundleArchive(file)
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['long', `long.tgz/${dir}`],
    ])
  })

  it('rejects a tar.gz containing a symlink', async () => {
    const file = await tarGzFile('linked.tgz', [
      tarFile('linked/SKILL.md', skillMd('linked')),
      { header: { name: 'linked/escape', type: 'symlink', linkname: '/etc/passwd', size: 0 } },
    ])
    await expect(bundleArchive(file)).rejects.toThrow(
      'Skill archive contains a symlink at linked/escape.',
    )
  })

  it('rejects a zip containing a symlink', async () => {
    const file = await zipFile(
      'linked.zip',
      { 'linked/SKILL.md': skillMd('linked') },
      { 'linked/data': '/etc/passwd' },
    )
    await expect(bundleArchive(file)).rejects.toThrow(
      'Skill archive contains a symlink at linked/data.',
    )
  })

  it('rejects a file that is not a zip', async () => {
    await expect(bundleArchive(new File(['not a zip'], 'broken.zip'))).rejects.toThrow()
  })
})

const testFileSystem: FileSystem = {
  name: 'test',
  get root() {
    return new FakeDirectoryEntry('', [])
  },
}

class FakeEntry implements FileSystemEntry {
  readonly filesystem = testFileSystem
  readonly fullPath: string
  readonly isDirectory: boolean
  readonly isFile: boolean
  readonly name: string

  constructor(name: string, isDirectory: boolean) {
    this.name = name
    this.fullPath = `/${name}`
    this.isDirectory = isDirectory
    this.isFile = !isDirectory
  }

  getParent() {
    return undefined
  }
}

class FakeFileEntry extends FakeEntry implements FileSystemFileEntry {
  readonly content: BlobPart

  constructor(name: string, content: BlobPart) {
    super(name, false)
    this.content = content
  }

  file(successCallback: FileCallback) {
    successCallback(new File([this.content], this.name))
  }
}

class FakeDirectoryEntry extends FakeEntry implements FileSystemDirectoryEntry {
  readonly children: FileSystemEntry[]

  constructor(name: string, children: FileSystemEntry[]) {
    super(name, true)
    this.children = children
  }

  createReader(): FileSystemDirectoryReader {
    const pending = [...this.children]
    return {
      readEntries: (successCallback) => {
        successCallback(pending.splice(0, 2))
      },
    }
  }

  getDirectory() {
    return undefined
  }

  getFile() {
    return undefined
  }
}

class UnreadableDirectoryEntry extends FakeDirectoryEntry {
  override createReader(): FileSystemDirectoryReader {
    throw new Error(`read ${this.name}`)
  }
}

describe('dropped sources', () => {
  beforeEach(() => {
    vi.stubGlobal('FileSystemFileEntry', FakeFileEntry)
    vi.stubGlobal('FileSystemDirectoryEntry', FakeDirectoryEntry)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('bundles dropped folders, loose SKILL.md files, and archives together', async () => {
    const entries = [
      new FakeDirectoryEntry('skills', [
        new FakeDirectoryEntry('one', [
          new FakeFileEntry('SKILL.md', skillMd('one')),
          new FakeDirectoryEntry('scripts', [
            new FakeFileEntry('a.py', 'a'),
            new FakeFileEntry('b.py', 'b'),
            new FakeFileEntry('c.py', 'c'),
          ]),
        ]),
        new FakeDirectoryEntry('two', [new FakeFileEntry('SKILL.md', skillMd('two'))]),
      ]),
      new FakeFileEntry(
        'packed.zip',
        await zipFile('packed.zip', { 'three/SKILL.md': skillMd('three') }),
      ),
    ]
    expect(skillSourceName({ kind: 'drop', entries })).toBe('2 items')
    const bundles = await bundleSource({ kind: 'drop', entries })
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['one', 'skills/one'],
      ['two', 'skills/two'],
      ['three', 'packed.zip/three'],
    ])
    expect(Object.keys(await archiveContents(bundles[0])).sort()).toEqual([
      'one/SKILL.md',
      'one/scripts/a.py',
      'one/scripts/b.py',
      'one/scripts/c.py',
    ])
  })

  it('keeps a dropped loose SKILL.md separate from dropped folders', async () => {
    const entries = [
      new FakeFileEntry('SKILL.md', skillMd('loose')),
      new FakeDirectoryEntry('folder', [new FakeFileEntry('SKILL.md', skillMd('folder'))]),
    ]
    const bundles = await bundleSource({ kind: 'drop', entries })
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['loose', ''],
      ['folder', 'folder'],
    ])
  })

  it('bundles a single dropped SKILL.md under its frontmatter name', async () => {
    const entries = [new FakeFileEntry('SKILL.md', skillMd('loose'))]
    expect(skillSourceName({ kind: 'drop', entries })).toBe('SKILL.md')
    const bundles = await bundleSource({ kind: 'drop', entries })
    expect(bundles.map((bundle) => bundle.label)).toEqual(['loose'])
  })

  it('finds skills in each dropped collection folder separately', async () => {
    const entries = [
      new FakeDirectoryEntry('first', [
        new FakeDirectoryEntry('one', [new FakeFileEntry('SKILL.md', skillMd('one'))]),
      ]),
      new FakeDirectoryEntry('second', [
        new FakeDirectoryEntry('two', [new FakeFileEntry('SKILL.md', skillMd('two'))]),
      ]),
    ]
    const bundles = await bundleSource({ kind: 'drop', entries })
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['one', 'first/one'],
      ['two', 'second/two'],
    ])
  })

  it('never reads ignored directories or directories without a SKILL.md', async () => {
    const entries = [
      new FakeDirectoryEntry('repo', [
        new FakeDirectoryEntry('one', [
          new FakeFileEntry('SKILL.md', skillMd('one')),
          new UnreadableDirectoryEntry('node_modules', []),
          new UnreadableDirectoryEntry('.git', []),
        ]),
        new UnreadableDirectoryEntry('node_modules', []),
        new UnreadableDirectoryEntry('.venv', []),
        new FakeDirectoryEntry('src', [new UnreadableDirectoryEntry('deep', [])]),
      ]),
    ]
    const bundles = await bundleSource({ kind: 'drop', entries })
    expect(bundles.map((bundle) => bundle.label)).toEqual(['one'])
    expect(Object.keys(await archiveContents(bundles[0]))).toEqual(['one/SKILL.md'])
  })
})
