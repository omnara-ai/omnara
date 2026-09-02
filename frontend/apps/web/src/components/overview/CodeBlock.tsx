import { type ReactNode, Suspense, useEffect, useState } from 'react'

import { CheckIcon, CopyIcon, XIcon } from '@/components/icons'
import { type CodeLanguage, Highlighted } from '@/components/overview/highlight'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

export type CodeSegment = string | { json: string }

export interface CodeContent {
  copy: string
  segments: CodeSegment[]
  language: CodeLanguage
}

export interface CodeTab {
  value: string
  label: string
  content: string | CodeContent
  emphasis?: boolean
  footer?: boolean
}

function toContent(content: string | CodeContent): CodeContent {
  return typeof content === 'string'
    ? { copy: content, segments: [content], language: 'shell' }
    : content
}

function prettyJson(json: string) {
  try {
    return JSON.stringify(JSON.parse(json), null, 2)
  } catch {
    return json
  }
}

function JsonSegment({ json }: { json: string }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <button
      type="button"
      aria-expanded={expanded}
      data-slot="json-toggle"
      className={cn(
        'text-left font-mono transition-[color,background-color]',
        expanded
          ? 'hover:text-foreground'
          : 'bg-muted text-foreground hover:bg-muted/70 rounded-md px-1.5 py-0.5',
      )}
      onClick={() => {
        setExpanded((prev) => !prev)
      }}
    >
      {expanded ? (
        <span>
          <Suspense fallback={prettyJson(json)}>
            <Highlighted code={prettyJson(json)} language="json" />
          </Suspense>
        </span>
      ) : (
        '{ … }'
      )}
    </button>
  )
}

type CopyState = 'idle' | 'copied' | 'failed'

const copyLabels = {
  idle: (label: string) => `Copy ${label}`,
  copied: (label: string) => `Copied ${label}`,
  failed: (label: string) => `Could not copy ${label}`,
} satisfies Record<CopyState, (label: string) => string>

function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<CopyState>('idle')

  useEffect(() => {
    if (state === 'idle') return
    const timer = window.setTimeout(() => {
      setState('idle')
    }, 2000)
    return () => {
      window.clearTimeout(timer)
    }
  }, [state])
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className={cn(
        'text-muted-foreground hover:text-foreground size-10 sm:size-8',
        state === 'failed' && 'text-destructive hover:text-destructive',
      )}
      aria-label={copyLabels[state](label)}
      onClick={() => {
        navigator.clipboard.writeText(text).then(
          () => {
            setState('copied')
          },
          () => {
            setState('failed')
          },
        )
      }}
    >
      {state === 'copied' ? <CheckIcon /> : state === 'failed' ? <XIcon /> : <CopyIcon />}
    </Button>
  )
}

function Code({
  content,
  emphasis = false,
  className,
}: {
  content: string | CodeContent
  emphasis?: boolean
  className?: string
}) {
  const { segments, language } = toContent(content)
  return (
    <pre
      className={cn(
        'code-highlight overflow-x-auto whitespace-pre-wrap break-words px-4 py-4 font-mono text-sm leading-6 sm:px-6 sm:py-5',
        emphasis ? 'text-foreground font-medium' : 'text-muted-foreground',
        className,
      )}
    >
      {segments.map((segment) =>
        typeof segment === 'string' ? (
          <span key={`text:${segment}`}>
            <Suspense fallback={segment}>
              <Highlighted code={segment} language={language} />
            </Suspense>
          </span>
        ) : (
          <JsonSegment key={`json:${segment.json}`} json={segment.json} />
        ),
      )}
    </pre>
  )
}

export function CodeBlock({
  content,
  label,
  className,
}: {
  content: string | CodeContent
  label: string
  className?: string
}) {
  return (
    <div className={cn('bg-card relative rounded-xl border', className)}>
      <div className="absolute right-3 top-3 sm:right-4 sm:top-4">
        <CopyButton text={toContent(content).copy} label={label} />
      </div>
      <div className="pr-12 sm:pr-14">
        <Code content={content} />
      </div>
    </div>
  )
}

export function CodeTabsBlock({
  tabs,
  label,
  footer,
  className,
}: {
  tabs: CodeTab[]
  label: string
  footer?: ReactNode
  className?: string
}) {
  const [value, setValue] = useState(tabs[0]?.value ?? '')
  const active = tabs.find((tab) => tab.value === value) ?? tabs[0]
  return (
    <div className={cn('bg-card relative rounded-xl border', className)}>
      <Tabs value={value} onValueChange={setValue}>
        <div className="flex flex-col items-stretch gap-2 border-b px-3 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-4">
          <TabsList
            variant="line"
            aria-label={label}
            className="w-full max-w-full justify-start gap-1 overflow-x-auto p-0 sm:w-fit"
          >
            {tabs.map((tab) => (
              <TabsTrigger
                key={tab.value}
                value={tab.value}
                className="text-muted-foreground data-[state=active]:bg-secondary! data-[state=active]:text-secondary-foreground! h-10 shrink-0 rounded-md px-3 transition-[color,background-color] after:hidden data-[state=active]:shadow-none sm:h-8 sm:px-3.5"
              >
                {tab.label}
              </TabsTrigger>
            ))}
          </TabsList>
          <div className="flex items-center gap-2 self-end sm:self-auto">
            {active && (
              <CopyButton
                text={toContent(active.content).copy}
                label={active.label.toLowerCase()}
              />
            )}
            {footer && active?.footer !== false && footer}
          </div>
        </div>
        {tabs.map((tab) => (
          <TabsContent key={tab.value} value={tab.value}>
            <Code content={tab.content} emphasis={tab.emphasis} className="pb-5 pt-3" />
          </TabsContent>
        ))}
      </Tabs>
    </div>
  )
}
