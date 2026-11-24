import { useContext } from 'react'

import { Button } from '@/components/ui/button'

import { AssistantContext } from './assistant-context'

const starterPrompts = ['Help me create my first agent', 'Explain what is on this page']

/** Shown until the user's first message; the title steps aside once the assistant has greeted. */
export function AssistantWelcome({ canAsk, greeted }: { canAsk: boolean; greeted: boolean }) {
  const { ask } = useContext(AssistantContext)
  return (
    <div className="m-auto flex flex-col items-center gap-4 py-6 text-center">
      {!greeted && <p className="font-medium">How can I help?</p>}
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
