import { gzipSync, strFromU8, strToU8, unzipSync, zipSync } from 'fflate'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  bundleArchive,
  bundleSkillMd,
  bundleSkills,
  bundleSource,
  checkSkillMd,
  type SkillBundle,
  skillSourceName,
} from './skill-bundles'

function skillMd(name: string) {
  return `---\nname: ${name}\ndescription: Does ${name} things.\n---\n\n# ${name}\n`
}

function source(path: string, content: string) {
  return { path, data: strToU8(content) }
}

async function archiveContents(bundle: SkillBundle) {
  const entries = unzipSync(new Uint8Array(await bundle.archive.arrayBuffer()))
  return Object.fromEntries(Object.entries(entries).map(([path, data]) => [path, strFromU8(data)]))
}

function tarGz(entries: { path: string; content?: string; type?: string }[]) {
  const blocks: Uint8Array[] = []
  for (const entry of entries) {
    const data = strToU8(entry.content ?? '')
    const header = new Uint8Array(512)
    header.set(strToU8(entry.path), 0)
    header.set(strToU8(data.length.toString(8).padStart(11, '0')), 124)
    header.set(strToU8(entry.type ?? '0'), 156)
    header.set(strToU8('ustar'), 257)
    blocks.push(header, data, new Uint8Array((512 - (data.length % 512)) % 512))
  }
  blocks.push(new Uint8Array(1024))
  const tar = new Uint8Array(blocks.reduce((total, block) => total + block.length, 0))
  let offset = 0
  for (const block of blocks) {
    tar.set(block, offset)
    offset += block.length
  }
  return gzipSync(tar)
}

function folderFile(path: string, content: string) {
  const file = new File([content], path.slice(path.lastIndexOf('/') + 1))
  Object.defineProperty(file, 'webkitRelativePath', { value: path })
  return file
}

describe('checkSkillMd', () => {
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
  it('zips one skill under a directory named after its frontmatter', async () => {
    const [bundle, ...rest] = bundleSkills(
      [
        source('My Skill/SKILL.md', skillMd('my-skill')),
        source('My Skill/scripts/run.py', 'print(1)'),
      ],
      'fallback',
    )
    expect(rest).toEqual([])
    expect(bundle?.label).toBe('my-skill')
    expect(bundle?.archive.name).toBe('my-skill.zip')
    expect(bundle && (await archiveContents(bundle))).toEqual({
      'my-skill/SKILL.md': skillMd('my-skill'),
      'my-skill/scripts/run.py': 'print(1)',
    })
  })

  it('splits a collection into one bundle per outermost SKILL.md directory', async () => {
    const bundles = bundleSkills(
      [
        source('skills/beta/SKILL.md', skillMd('beta')),
        source('skills/alpha/SKILL.md', skillMd('alpha')),
        source('skills/alpha/templates/SKILL.md', 'nested template'),
        source('skills/README.md', 'collection readme'),
        source('skills/alpha/.DS_Store', ''),
        source('__MACOSX/skills/alpha/._SKILL.md', ''),
      ],
      'fallback',
    )
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['alpha', 'skills/alpha'],
      ['beta', 'skills/beta'],
    ])
    const [alpha] = bundles
    expect(alpha && Object.keys(await archiveContents(alpha)).sort()).toEqual([
      'alpha/SKILL.md',
      'alpha/templates/SKILL.md',
    ])
  })

  it('names a top-level SKILL.md skill from frontmatter, then flags the fallback', async () => {
    const [named] = bundleSkills([source('SKILL.md', skillMd('root-skill'))], 'fallback')
    expect(named && Object.keys(await archiveContents(named))).toEqual(['root-skill/SKILL.md'])
    const [unnamed] = bundleSkills([source('skill.md', '# no frontmatter')], 'fallback')
    expect(unnamed && Object.keys(await archiveContents(unnamed))).toEqual(['fallback/skill.md'])
    expect(unnamed?.problem).toBe("SKILL.md is missing YAML frontmatter delimited by '---'.")
  })

  it('returns nothing when no SKILL.md is present', () => {
    expect(bundleSkills([source('notes/README.md', 'hi')], 'fallback')).toEqual([])
  })
})

describe('bundleSkillMd', () => {
  it('zips pasted SKILL.md content under its frontmatter name', async () => {
    const bundle = bundleSkillMd(skillMd('pasted'))
    expect(bundle.label).toBe('pasted')
    expect(await archiveContents(bundle)).toEqual({ 'pasted/SKILL.md': skillMd('pasted') })
    expect(bundle.problem).toBeUndefined()
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
    const [, two] = bundles
    expect(two && (await archiveContents(two))).toEqual({
      'two/SKILL.md': skillMd('two'),
      'two/ref.md': 'reference',
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
    const [, second] = bundles
    expect(second && (await archiveContents(second))).toEqual({
      'second/SKILL.md': skillMd('second'),
    })
  })

  it('bundles chosen SKILL.md files and archives together', async () => {
    const files = [
      new File([skillMd('loose')], 'SKILL.md'),
      new File([zipSync({ 'packed/SKILL.md': strToU8(skillMd('packed')) })], 'packed.zip'),
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
    const zip = zipSync({
      'bundle/a/SKILL.md': strToU8(skillMd('a')),
      'bundle/b/SKILL.md': strToU8(skillMd('b')),
      'bundle/b/': new Uint8Array(),
    })
    const bundles = await bundleArchive(new File([zip], 'bundle.zip'))
    expect(bundles.map((bundle) => bundle.label)).toEqual(['a', 'b'])
  })

  it('reads every skill in a tar.gz and rebundles it under the frontmatter name', async () => {
    const file = new File(
      [
        tarGz([
          { path: './set/', type: '5' },
          { path: './set/Release Notes/SKILL.md', content: skillMd('release-notes') },
          { path: './set/Release Notes/template.md', content: 'template' },
          { path: './set/slides/SKILL.md', content: skillMd('slides') },
        ]),
      ],
      'set.tar.gz',
    )
    const bundles = await bundleArchive(file)
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['release-notes', 'set.tar.gz/set/Release Notes'],
      ['slides', 'set.tar.gz/set/slides'],
    ])
    const [notes] = bundles
    expect(notes && (await archiveContents(notes))).toEqual({
      'release-notes/SKILL.md': skillMd('release-notes'),
      'release-notes/template.md': 'template',
    })
  })

  it('rejects a tar.gz containing a symlink', async () => {
    const file = new File(
      [
        tarGz([
          { path: 'linked/SKILL.md', content: skillMd('linked') },
          { path: 'linked/escape', type: '2' },
        ]),
      ],
      'linked.tgz',
    )
    await expect(bundleArchive(file)).rejects.toThrow('symlink')
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
      new FakeFileEntry('packed.zip', zipSync({ 'three/SKILL.md': strToU8(skillMd('three')) })),
    ]
    expect(skillSourceName({ kind: 'drop', entries })).toBe('2 items')
    const bundles = await bundleSource({ kind: 'drop', entries })
    expect(bundles.map((bundle) => [bundle.label, bundle.sourcePath])).toEqual([
      ['one', 'skills/one'],
      ['two', 'skills/two'],
      ['three', 'packed.zip/three'],
    ])
    const [one] = bundles
    expect(one && Object.keys(await archiveContents(one)).sort()).toEqual([
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
})
