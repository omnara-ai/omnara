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

it('sends the next question after one could not be sent', async () => {
  const queue = createAskQueue()
  const started: string[] = []
  queue.register((text) => {
    started.push(text)
    return text === 'first' ? Promise.reject(new Error('offline')) : Promise.resolve()
  })
  queue.ask('first')
  queue.ask('second')
  await expect.poll(() => started).toEqual(['first', 'second'])
})
