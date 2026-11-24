import { expect, it } from 'vitest'

import { createAskQueue } from './assistant-context'

it('holds questions until the conversation can take them, then sends them one at a time', async () => {
  const queue = createAskQueue()
  const started: string[] = []
  const finish: (() => void)[] = []
  queue.ask('first')
  queue.ask('second')
  await Promise.resolve()
  expect(started).toEqual([])

  queue.register((text) => {
    started.push(text)
    return new Promise<void>((resolve) => {
      finish.push(resolve)
    })
  })
  await expect.poll(() => started).toEqual(['first'])
  finish[0]?.()
  await expect.poll(() => started).toEqual(['first', 'second'])
})
