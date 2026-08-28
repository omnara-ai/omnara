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

const copyLabels: Record<CopyState, (label: string) => string> = {
  idle: (label) => `Copy ${label}`,
  copied: (label) => `Copied ${label}`,
  failed: (label) => `Could not copy ${label}`,
}

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
        'text-muted-foreground hover:text-foreground size-8',
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
        'code-highlight overflow-x-auto whitespace-pre-wrap break-words px-6 py-5 font-mono text-sm leading-6',
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
      <div className="absolute right-4 top-4">
        <CopyButton text={toContent(content).copy} label={label} />
      </div>
      <div className="pr-14">
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
        <div className="flex items-center justify-between gap-2 border-b px-4 py-3">
          <TabsList variant="line" aria-label={label} className="gap-1 p-0">
            {tabs.map((tab) => (
              <TabsTrigger
                key={tab.value}
                value={tab.value}
                className="text-muted-foreground data-[state=active]:bg-secondary! data-[state=active]:text-secondary-foreground! h-8 rounded-md px-3.5 transition-[color,background-color] after:hidden data-[state=active]:shadow-none"
              >
                {tab.label}
              </TabsTrigger>
            ))}
          </TabsList>
          <div className="flex items-center gap-2">
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
