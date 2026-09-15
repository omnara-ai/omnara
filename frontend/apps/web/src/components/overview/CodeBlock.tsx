import { type ReactNode, Suspense, useEffect, useLayoutEffect, useRef, useState } from 'react'

import { ArrowRight, CheckIcon, CopyIcon, XIcon } from '@/components/icons'
import { type CodeLanguage, Highlighted } from '@/components/overview/highlight'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

export type CodeSegment = { text: string } | { json: string }

export interface CodeContent {
  copy: string
  segments: CodeSegment[]
  language: CodeLanguage
}

export interface CodeTab {
  value: string
  label: string
  content: CodeContent
  emphasis?: boolean
  footer?: boolean
}

function prettyJson(json: string) {
  try {
    return JSON.stringify(JSON.parse(json), null, 2)
  } catch {
    return json
  }
}

const jsonBraceClass = 'text-muted-foreground'

function JsonSegment({ json }: { json: string }) {
  const [expanded, setExpanded] = useState(false)
  if (expanded) {
    return (
      <button
        type="button"
        aria-expanded="true"
        aria-label="Collapse"
        data-slot="json-toggle"
        className="hover:bg-muted/40 -mx-1 rounded px-1 text-left font-mono transition-colors"
        onClick={() => {
          setExpanded(false)
        }}
      >
        <Suspense fallback={prettyJson(json)}>
          <Highlighted code={prettyJson(json)} language="json" />
        </Suspense>
      </button>
    )
  }
  return (
    <>
      <span className={jsonBraceClass}>{'{ '}</span>
      <button
        type="button"
        aria-expanded="false"
        aria-label="Expand"
        data-slot="json-toggle"
        className="bg-muted text-muted-foreground hover:border-ring hover:bg-muted/70 hover:text-foreground inline-flex items-center rounded-md border px-2 py-0.5 align-middle text-[12px] leading-none transition-colors"
        onClick={() => {
          setExpanded(true)
        }}
      >
        …
      </button>
      <span className={jsonBraceClass}>{' }'}</span>
    </>
  )
}

const codePanelClass =
  'bg-card/70 relative overflow-hidden rounded-2xl shadow-[0_10px_32px_-20px_rgba(0,0,0,0.14)] backdrop-blur-md'

function CodePanelRing() {
  return (
    <div
      aria-hidden="true"
      className="code-panel-ring pointer-events-none absolute inset-0 rounded-2xl"
    />
  )
}

function useMeasuredHeight() {
  const ref = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState<number>()
  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const update = () => {
      setHeight(element.offsetHeight)
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => {
      observer.disconnect()
    }
  }, [])
  return { ref, height }
}

type CopyState = 'idle' | 'copied' | 'failed'

const copyLabels = {
  idle: (label: string) => `Copy ${label}`,
  copied: (label: string) => `Copied ${label}`,
  failed: (label: string) => `Could not copy ${label}`,
} satisfies Record<CopyState, (label: string) => string>

export function CopyButton({ text, label }: { text: string; label: string }) {
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
  content: CodeContent
  emphasis?: boolean
  className?: string
}) {
  const { segments, language } = content
  return (
    <pre
      className={cn(
        'code-highlight overflow-x-auto whitespace-pre px-4 py-4 font-mono text-[12.5px] leading-[1.8] sm:px-6 sm:py-5',
        emphasis ? 'text-foreground font-medium' : 'text-foreground/85',
        className,
      )}
    >
      {segments.map((segment) =>
        'json' in segment ? (
          <JsonSegment key={`json:${segment.json}`} json={segment.json} />
        ) : (
          <span key={`text:${segment.text}`}>
            <Suspense fallback={segment.text}>
              <Highlighted code={segment.text} language={language} />
            </Suspense>
          </span>
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
  content: CodeContent
  label: string
  className?: string
}) {
  return (
    <div className={cn(codePanelClass, className)}>
      <CodePanelRing />
      <div className="absolute right-2.5 top-2">
        <CopyButton text={content.copy} label={label} />
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
  const { ref: bodyRef, height: bodyHeight } = useMeasuredHeight()
  return (
    <div className={cn(codePanelClass, className)}>
      <CodePanelRing />
      <Tabs value={value} onValueChange={setValue}>
        <div className="flex flex-col items-stretch gap-2 py-2 pl-2.5 pr-2.5 sm:flex-row sm:items-center sm:justify-between">
          <TabsList
            variant="line"
            aria-label={label}
            className="w-full max-w-full justify-start gap-1 overflow-x-auto p-0 sm:w-fit"
          >
            {tabs.map((tab) => (
              <TabsTrigger
                key={tab.value}
                value={tab.value}
                className="text-muted-foreground hover:text-foreground data-[state=active]:bg-foreground/10! data-[state=active]:text-foreground! dark:data-[state=active]:bg-foreground/15! data-[state=active]:shadow-xs h-9 shrink-0 rounded-md px-3 text-[12.5px] font-medium transition-[color,background-color] after:hidden sm:h-7"
              >
                {tab.label}
              </TabsTrigger>
            ))}
          </TabsList>
          <div className="flex items-center gap-1.5 self-end sm:self-auto">
            {active?.value === 'cli' && (
              <span className="text-primary hidden items-center gap-1 text-[12px] sm:flex dark:text-[color-mix(in_oklab,var(--primary)_60%,white)]">
                Copy and run in your terminal
                <ArrowRight className="size-3.5" aria-hidden="true" />
              </span>
            )}
            {active && <CopyButton text={active.content.copy} label={active.label.toLowerCase()} />}
            {footer && active?.footer !== false && footer}
          </div>
        </div>
        <div className="code-panel-body overflow-hidden" style={{ height: bodyHeight }}>
          <div ref={bodyRef}>
            {active && (
              <TabsContent value={active.value}>
                <Code
                  content={active.content}
                  emphasis={active.emphasis}
                  className="pb-4 pt-1 sm:pb-5 sm:pt-1"
                />
              </TabsContent>
            )}
          </div>
        </div>
      </Tabs>
    </div>
  )
}
