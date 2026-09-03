import { render } from 'ink'

import { type ChatTarget } from './chat-session.ts'
import { Chat } from './chat-ui.tsx'

export async function runChat(target: ChatTarget): Promise<void> {
  const app = render(<Chat target={target} />)
  await app.waitUntilExit()
}
