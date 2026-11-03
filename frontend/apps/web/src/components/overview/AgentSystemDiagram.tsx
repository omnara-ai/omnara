import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react'

import { AuroraPanel } from '@/components/aurora/AuroraPanel'
import { BrandMark } from '@/components/brand/OmnaraMark'
import {
  type CallsNode,
  type DiagramNode,
  type Example,
  examples,
  type GroupNode,
} from '@/components/overview/agentSystemExamples'
import {
  CALL_CHAR_W,
  CALL_LINE_H,
  CALL_PAD_X,
  CALL_PAD_Y,
  CALL_TAG_H,
  CHIP_GAP,
  CHIP_H,
  CHIP_PAD_X,
  chipTop,
  DIAGRAM_H,
  type DrawnExample,
  edgePaths,
  GRID_W,
  GROUP_PAD,
  ICON_SIZE,
  ICON_W,
  isGroup,
  isSolo,
  isTileRow,
  layout,
  LOOP_OUT,
  MIN_GRID_SCALE,
  SCALE,
  SOLO_PAD_X,
  stacked,
  STACKED_H,
} from '@/components/overview/agentSystemLayout'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

/** How long each example shows before the next, until someone picks one. */
const ROTATE_MS = 7000

function subscribeReducedMotion(onChange: () => void) {
  const query = window.matchMedia('(prefers-reduced-motion: reduce)')
  query.addEventListener('change', onChange)
  return () => {
    query.removeEventListener('change', onChange)
  }
}

function usePrefersReducedMotion() {
  return useSyncExternalStore(
    subscribeReducedMotion,
    () => window.matchMedia('(prefers-reduced-motion: reduce)').matches,
  )
}

/**
 * Whether the card is too narrow for the grid to read, by its own width rather than the
 * screen's, since the sidebar takes a share of tablet-sized screens.
 */
function useStacked() {
  const ref = useRef<HTMLElement>(null)
  const [stack, setStack] = useState(false)
  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const update = () => {
      setStack(element.offsetWidth < GRID_W * MIN_GRID_SCALE)
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => {
      observer.disconnect()
    }
  }, [])
  return { ref, stack }
}

export function AgentSystemDiagram() {
  const reducedMotion = usePrefersReducedMotion()
  const { ref, stack } = useStacked()
  const [index, setIndex] = useState(0)
  const [picked, setPicked] = useState(false)
  const [hovered, setHovered] = useState(false)
  const rotate = !reducedMotion && !picked && !hovered

  useEffect(() => {
    if (!rotate) return
    const timer = window.setInterval(() => {
      setIndex((current) => (current + 1) % examples.length)
    }, ROTATE_MS)
    return () => {
      window.clearInterval(timer)
    }
  }, [rotate])

  const example = examples[index] ?? examples[0]
  if (!example) return null

  return (
    <section
      ref={ref}
      className="flex flex-col gap-3"
      onPointerEnter={() => {
        setHovered(true)
      }}
      onPointerLeave={() => {
        setHovered(false)
      }}
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="type-card-title">What you can build</h3>
        <Tabs
          className="w-full sm:w-auto"
          value={String(index)}
          onValueChange={(value) => {
            setIndex(Number(value))
            setPicked(true)
          }}
        >
          <TabsList aria-label="Example" className="w-full sm:w-fit">
            {examples.map((option, optionIndex) => (
              <TabsTrigger key={option.name} value={String(optionIndex)} className="sm:px-3">
                {option.name}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      </div>
      <AuroraPanel className="bg-card/25 overflow-hidden shadow-none">
        <div className="relative flex overflow-x-auto px-4 py-6 sm:py-8">
          <SystemDiagram
            key={example.name}
            example={example}
            stack={stack}
            animate={!reducedMotion}
          />
        </div>
      </AuroraPanel>
    </section>
  )
}

function describe(node: DiagramNode) {
  if (!isGroup(node)) return `${node.title}: ${node.calls.join(', ')}`
  return `${node.label ?? 'Omnara'}: ${node.chips.map((chip) => chip.name).join(', ')}`
}

function SystemDiagram({
  example: shown,
  stack,
  animate,
}: {
  example: Example
  stack: boolean
  animate: boolean
}) {
  const example: DrawnExample = stack ? stacked(shown) : shown
  const { placed, width, top, height } = layout(example)
  const diagramH = stack ? STACKED_H : DIAGRAM_H
  const originY = top - (diagramH - height) / 2 - 12
  // Room either side for links looping round the stack, so it stays centred.
  const loopRoom = example.edges.some((edge) => edge.loop) ? LOOP_OUT : 0
  const viewW = width + 24 + loopRoom * 2
  return (
    <svg
      viewBox={`${String(-12 - loopRoom)} ${String(originY)} ${String(viewW)} ${String(diagramH + 24)}`}
      // A fixed height keeps the card from resizing between examples. Wider
      // examples shrink to fit the card, and stack before they shrink too far to read.
      height={(diagramH + 24) * SCALE}
      style={{ maxWidth: viewW * SCALE }}
      role="img"
      aria-label={`${example.name}. ${example.nodes.map(describe).join('. ')}`}
      className="animate-in fade-in-0 mx-auto block w-full shrink-0 overflow-visible font-mono duration-500"
    >
      {example.edges.map((edge, index) => {
        const from = placed.get(edge.from)
        const to = placed.get(edge.to)
        if (!from || !to) return null
        const { line, motion } = edgePaths(from, to, edge, width + LOOP_OUT)
        return (
          <g key={line}>
            <path
              d={line}
              fill="none"
              strokeWidth="1"
              className="stroke-primary/45 dark:stroke-primary/60"
            />
            {animate && (
              <circle r="2" className="fill-primary/90">
                <animateMotion
                  dur="3.2s"
                  begin={`${String(-index * 0.55)}s`}
                  repeatCount="indefinite"
                  path={motion}
                />
              </circle>
            )}
          </g>
        )
      })}
      {[...placed.values()].map((item) =>
        isGroup(item.node) ? (
          isSolo(item.node) ? (
            <Solo key={item.node.id} group={item.node} {...item} />
          ) : (
            <Group key={item.node.id} group={item.node} {...item} />
          )
        ) : (
          <Calls key={item.node.id} calls={item.node} {...item} />
        ),
      )}
    </svg>
  )
}

interface BoxProps {
  x: number
  y: number
  w: number
  h: number
}

function Solo({ group, x, y, w, h }: BoxProps & { group: GroupNode }) {
  const chip = group.chips[0]
  if (!chip) return null
  return (
    <g>
      <rect
        x={x}
        y={y}
        width={w}
        height={h}
        rx="8"
        strokeDasharray="4 3"
        className="fill-card stroke-foreground/25 dark:stroke-foreground/35"
      />
      <chip.icon
        x={x + SOLO_PAD_X}
        y={y + (h - ICON_SIZE) / 2}
        width={ICON_SIZE}
        height={ICON_SIZE}
        strokeWidth={1.75}
        className="text-foreground/60"
      />
      <text
        x={x + SOLO_PAD_X + ICON_W}
        y={y + h / 2}
        dominantBaseline="central"
        className="fill-foreground/75 font-sans text-[12px]"
      >
        {group.label}
      </text>
    </g>
  )
}

function Group({ group, x, y, w, h }: BoxProps & { group: GroupNode }) {
  const omnara = group.label === undefined
  const tiles = isTileRow(group)
  return (
    <g>
      {omnara && (
        <ellipse
          cx={x + w / 2}
          cy={y + h / 2}
          rx={w * 0.9}
          ry={h * 0.75}
          className="fill-primary/[0.14] dark:fill-primary/[0.22] blur-xl"
        />
      )}
      <rect
        x={x}
        y={y}
        width={w}
        height={h}
        rx="8"
        strokeDasharray={omnara ? undefined : '4 3'}
        className={cn(
          'fill-card',
          omnara
            ? 'stroke-primary/50 drop-shadow-[0_6px_16px_rgba(0,0,0,0.08)]'
            : 'stroke-foreground/25 dark:stroke-foreground/35',
        )}
      />
      {omnara ? (
        <>
          <foreignObject x={x + GROUP_PAD - 1} y={y + GROUP_PAD - 2} width="14" height="14">
            <BrandMark className="size-3.5" />
          </foreignObject>
          <text
            x={x + GROUP_PAD + 17}
            y={y + GROUP_PAD + 5}
            dominantBaseline="central"
            className="fill-foreground/80 font-sans text-[12px] font-medium"
          >
            Omnara
          </text>
        </>
      ) : (
        <>
          {group.icon && (
            <group.icon
              x={x + GROUP_PAD}
              y={y + GROUP_PAD - 1}
              width={12}
              height={12}
              strokeWidth={1.75}
              className="text-muted-foreground"
            />
          )}
          <text
            x={x + GROUP_PAD + 18}
            y={y + GROUP_PAD + 5}
            dominantBaseline="central"
            className="fill-muted-foreground text-[10px] tracking-[0.14em] uppercase"
          >
            {group.label}
          </text>
        </>
      )}
      {group.chips.map((chip, index) => {
        const chipX = tiles ? x + GROUP_PAD + index * (CHIP_H + CHIP_GAP) : x + GROUP_PAD
        const chipY = chipTop(y, tiles ? 0 : index)
        const iconX = tiles ? chipX + (CHIP_H - ICON_SIZE) / 2 : chipX + CHIP_PAD_X
        return (
          <g key={chip.name}>
            <title>{chip.name}</title>
            <rect
              x={chipX}
              y={chipY}
              width={tiles ? CHIP_H : w - GROUP_PAD * 2}
              height={CHIP_H}
              rx="6"
              className={
                omnara
                  ? 'fill-primary/[0.09] stroke-primary/[0.18]'
                  : 'fill-foreground/[0.03] stroke-foreground/10'
              }
            />
            <chip.icon
              x={iconX}
              y={chipY + (CHIP_H - ICON_SIZE) / 2}
              width={ICON_SIZE}
              height={ICON_SIZE}
              strokeWidth={1.75}
              className={omnara ? 'text-primary' : 'text-foreground/60'}
            />
            {chip.short && (
              <text
                x={iconX + ICON_W}
                y={chipY + CHIP_H / 2}
                dominantBaseline="central"
                className="fill-foreground/75 font-sans text-[12px]"
              >
                {chip.short}
              </text>
            )}
          </g>
        )
      })}
    </g>
  )
}

/**
 * Actions crossing between entities: a solid, tinted block of code with a tag above
 * it, set apart from the dashed entity cards.
 */
function Calls({ calls, x, y, w, h }: BoxProps & { calls: CallsNode }) {
  const boxY = y + CALL_TAG_H
  const boxH = h - CALL_TAG_H
  const boxW = Math.max(...calls.calls.map((call) => call.length)) * CALL_CHAR_W + CALL_PAD_X * 2
  const boxX = x + (w - boxW) / 2
  return (
    <g>
      <text
        x={boxX + 2}
        y={y + CALL_TAG_H / 2 - 2}
        dominantBaseline="central"
        className="fill-primary/80 text-[9.5px] tracking-[0.14em] uppercase"
      >
        {calls.title}
      </text>
      <rect x={boxX} y={boxY} width={boxW} height={boxH} rx="5" className="fill-card" />
      <rect
        x={boxX}
        y={boxY}
        width={boxW}
        height={boxH}
        rx="5"
        className="fill-primary/[0.06] stroke-primary/35 dark:fill-primary/[0.1]"
      />
      {calls.calls.map((call, index) => {
        const [method, ...rest] = call.split(' ')
        const http = rest.length > 0
        return (
          <text
            key={call}
            x={boxX + CALL_PAD_X}
            y={boxY + CALL_PAD_Y + CALL_LINE_H / 2 + index * CALL_LINE_H}
            dominantBaseline="central"
            className="fill-foreground/80 text-[12px]"
          >
            {http ? (
              <>
                <tspan className="fill-primary">{method}</tspan> {rest.join(' ')}
              </>
            ) : (
              call
            )}
          </text>
        )
      })}
    </g>
  )
}
