import { useContext } from 'react'

import { Button } from '@/components/ui/button'

import { AssistantContext } from './assistant-context'

const starterPrompts = ['Help me create my first agent', 'Explain what is on this page']

/** What an assistant nobody has spoken to yet shows in place of a conversation. */
export function AssistantWelcome({ canAsk }: { canAsk: boolean }) {
  const { ask } = useContext(AssistantContext)
  return (
    <div className="m-auto flex flex-col items-center gap-4 py-6 text-center">
      <p className="font-medium">How can I help?</p>
      {canAsk && (
        <div className="flex flex-wrap justify-center gap-2">
          {starterPrompts.map((prompt) => (
            <Button
              key={prompt}
              variant="outline"
              size="sm"
              onClick={() => {
                ask(prompt)
              }}
            >
              {prompt}
            </Button>
          ))}
        </div>
      )}
    </div>
  )
}
