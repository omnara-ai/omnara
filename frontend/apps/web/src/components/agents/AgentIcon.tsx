import { useId } from 'react'

import {
  type AgentIconSpec,
  type IconCell,
  type IconTexture,
  type MirrorAxis,
  tintColor,
} from '@/lib/agent-icon'
import type { CssVariables } from '@/lib/css'
import { cn } from '@/lib/utils'

const cellSize = 6
const paddingRatio = 0.1
const texturePeriod = cellSize / 3
const sweepDimOpacity = 0.55
const sweepPeriodRatio = 1.5
const sweepStops = Array.from({ length: 13 }, (_, index) => {
  const offset = index / 12
  const depth = (1 - Math.cos(2 * Math.PI * offset)) / 2
  return { offset, opacity: 1 - (1 - sweepDimOpacity) * depth }
})
const sweepAngles = {
  vertical: 0,
  horizontal: -90,
  diagonal: -45,
  antidiagonal: 45,
} satisfies Record<MirrorAxis, number>

export function AgentIcon({
  icon,
  animated = false,
  className,
}: {
  icon: AgentIconSpec
  animated?: boolean
  className?: string
}) {
  const id = useId()
  const textureId = `${id}-texture`
  const sweepGradientId = `${id}-sweep-gradient`
  const sweepMaskId = `${id}-sweep-mask`
  const { style, axis, columns, rows, cells, sweep } = icon.pattern
  const sweeping = animated && sweep !== undefined
  const gridWidth = cellSize * columns
  const gridHeight = cellSize * rows
  const viewBoxSize = Math.max(gridWidth, gridHeight) * (1 + paddingRatio * 2)
  const gridLeft = (viewBoxSize - gridWidth) / 2
  const gridTop = (viewBoxSize - gridHeight) / 2
  const color = tintColor(icon.tint)
  const center = viewBoxSize / 2
  const sweepPeriod = viewBoxSize * sweepPeriodRatio
  const sweepStyle: CssVariables = {
    '--agent-sweep-distance': `${sweepPeriod}px`,
    animationDirection: sweep === 'backward' ? 'reverse' : 'normal',
  }
  const cellsPath = (textured: boolean) =>
    cells
      .filter((cell) => cell.textured === textured)
      .map((cell) =>
        glyphPath(cell, gridLeft + cell.column * cellSize, gridTop + cell.row * cellSize),
      )
      .join('')
  return (
    <svg
      viewBox={`0 0 ${viewBoxSize} ${viewBoxSize}`}
      className={cn('size-9 shrink-0 rounded-[4px]', className)}
      style={{ backgroundColor: tintColor(icon.tint, 0.16) }}
      aria-hidden="true"
    >
      <defs>
        <pattern
          id={textureId}
          x={gridLeft}
          y={gridTop}
          width={texturePeriod}
          height={texturePeriod}
          patternUnits="userSpaceOnUse"
        >
          <g fill={color}>
            <TextureMarks texture={style.texture} period={texturePeriod} />
          </g>
        </pattern>
        {sweeping && (
          <>
            <linearGradient
              id={sweepGradientId}
              gradientUnits="userSpaceOnUse"
              x1={0}
              y1={0}
              x2={0}
              y2={sweepPeriod}
              spreadMethod="repeat"
            >
              {sweepStops.map((stop) => (
                <stop
                  key={stop.offset}
                  offset={stop.offset}
                  stopColor="white"
                  stopOpacity={stop.opacity}
                />
              ))}
            </linearGradient>
            <mask
              id={sweepMaskId}
              maskUnits="userSpaceOnUse"
              x={0}
              y={0}
              width={viewBoxSize}
              height={viewBoxSize}
            >
              <g transform={`rotate(${sweepAngles[axis]} ${center} ${center})`}>
                <rect
                  className="animate-agent-sweep motion-reduce:animate-none"
                  style={sweepStyle}
                  x={-viewBoxSize}
                  y={-2 * viewBoxSize}
                  width={3 * viewBoxSize}
                  height={4 * viewBoxSize}
                  fill={`url(#${sweepGradientId})`}
                />
              </g>
            </mask>
          </>
        )}
      </defs>
      <g mask={sweeping ? `url(#${sweepMaskId})` : undefined}>
        <path d={cellsPath(false)} fill={color} />
        <path d={cellsPath(true)} fill={`url(#${textureId})`} />
      </g>
    </svg>
  )
}

function TextureMarks({ texture, period }: { texture: IconTexture; period: number }) {
  switch (texture) {
    case 'dots':
      return <circle cx={period / 2} cy={period / 2} r={period * 0.3} />
    case 'lines':
      return <rect width={period} height={period * 0.4} />
    case 'columns':
      return <rect width={period * 0.4} height={period} />
  }
}

function glyphPath({ glyph, corner }: IconCell, x: number, y: number) {
  const right = corner === 1 || corner === 2
  const bottom = corner === 2 || corner === 3
  const cornerX = right ? x + cellSize : x
  const cornerY = bottom ? y + cellSize : y
  const otherX = right ? x : x + cellSize
  const otherY = bottom ? y : y + cellSize
  switch (glyph) {
    case 'square':
      return `M${x} ${y}h${cellSize}v${cellSize}h${-cellSize}Z`
    case 'triangle':
      return `M${cornerX} ${cornerY}L${otherX} ${cornerY}L${cornerX} ${otherY}Z`
    case 'quarter': {
      const sweep = corner % 2 === 0 ? 1 : 0
      return `M${cornerX} ${cornerY}L${otherX} ${cornerY}A${cellSize} ${cellSize} 0 0 ${sweep} ${cornerX} ${otherY}Z`
    }
  }
}
