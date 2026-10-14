import { describe, expect, it } from 'vitest'

import { skillFileTree } from '@/lib/skill-file-tree'

describe('skillFileTree', () => {
  it('nests paths into folders listed before files, each sorted by name', () => {
    expect(
      skillFileTree([
        'SKILL.md',
        'scripts/deploy.sh',
        'reference/flags.md',
        'scripts/lib/util.sh',
        'LICENSE',
        'reference/api.md',
      ]),
    ).toEqual([
      {
        name: 'reference',
        path: 'reference',
        children: [
          { name: 'api.md', path: 'reference/api.md' },
          { name: 'flags.md', path: 'reference/flags.md' },
        ],
      },
      {
        name: 'scripts',
        path: 'scripts',
        children: [
          {
            name: 'lib',
            path: 'scripts/lib',
            children: [{ name: 'util.sh', path: 'scripts/lib/util.sh' }],
          },
          { name: 'deploy.sh', path: 'scripts/deploy.sh' },
        ],
      },
      { name: 'LICENSE', path: 'LICENSE' },
      { name: 'SKILL.md', path: 'SKILL.md' },
    ])
  })
})
