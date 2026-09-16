import { createReadStream, createWriteStream, type ReadStream } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { type Readable, Transform } from 'node:stream'
import { pipeline } from 'node:stream/promises'

import type { ProviderWorkReservation } from '../types'
import type { WorkByteBudget } from '../work-budget'
import { InvalidOperationError, type OperationArtifactMetadata } from './envelope'

export interface OperationArtifact extends OperationArtifactMetadata {
  sizeBytes: number
  /** Valid only during execute; reopens for explicitly safe provider retries. */
  open(): ReadStream
}

/** One request's scratch files. Paths and caller filenames are never combined. */
export class OperationFiles {
  private directory?: string
  private readonly readers = new Set<ReadStream>()
  private readonly reservation: ProviderWorkReservation
  private bytes = 0
  private closed = false

  constructor(
    private readonly root: string,
    budget: WorkByteBudget,
    private readonly signal: AbortSignal,
  ) {
    this.reservation = budget.reserve(0)
  }

  async save(
    source: Readable,
    metadata: OperationArtifactMetadata,
    index: number,
  ): Promise<OperationArtifact> {
    this.signal.throwIfAborted()
    this.directory ??= await mkdtemp(join(this.root, 'omnara-operation-'))
    this.signal.throwIfAborted()
    const filename = join(this.directory, String(index))
    let sizeBytes = 0
    const counted = new Transform({
      highWaterMark: 32 * 1024,
      transform: (chunk: Buffer, _encoding, callback) => {
        sizeBytes += chunk.byteLength
        try {
          // Reserve actual disk bytes before allowing the filesystem write.
          this.reservation.resize(this.bytes + chunk.byteLength)
          this.bytes += chunk.byteLength
          callback(null, chunk)
        } catch {
          callback(new TemporaryStorageFullError())
        }
      },
    })
    await pipeline(
      source,
      counted,
      createWriteStream(filename, {
        flags: 'wx',
        mode: 0o600,
        highWaterMark: 32 * 1024,
      }),
      { signal: this.signal },
    )
    return {
      ...metadata,
      sizeBytes,
      open: () => {
        this.signal.throwIfAborted()
        if (this.closed || this.readers.size >= 20) throw new InvalidOperationError()
        const reader = createReadStream(filename, { highWaterMark: 32 * 1024, signal: this.signal })
        this.readers.add(reader)
        reader.once('close', () => {
          this.readers.delete(reader)
        })
        // Cancellation may destroy an opened stream before an adapter starts
        // consuming it. Observe errors here too, without swallowing read errors.
        reader.on('error', () => undefined)
        return reader
      },
    }
  }

  async close(): Promise<void> {
    this.closed = true
    await Promise.all(
      [...this.readers].map(
        (reader) =>
          new Promise<void>((resolve) => {
            if (reader.closed) {
              resolve()
              return
            }
            reader.once('close', resolve)
            reader.destroy()
          }),
      ),
    )
    this.readers.clear()
    // Keep the reservation on filesystem cleanup failure: never admit more
    // files while their supposedly freed bytes may still exist on disk.
    if (this.directory) await rm(this.directory, { recursive: true, force: true })
    this.reservation.release()
  }
}

export class TemporaryStorageFullError extends Error {
  constructor() {
    super('operation temporary storage is full')
  }
}
