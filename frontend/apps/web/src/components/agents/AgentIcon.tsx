import { useId } from 'react'

import { type AgentIconSpec, type IconCell, type IconTexture, tintColor } from '@/lib/agent-icon'
import { cn } from '@/lib/utils'

const cellSize = 6
const paddingRatio = 0.1
const texturePeriod = cellSize / 3

export function AgentIcon({ icon, className }: { icon: AgentIconSpec; className?: string }) {
  const textureId = useId()
  const { style, columns, rows, cells } = icon.pattern
  const gridWidth = cellSize * columns
  const gridHeight = cellSize * rows
  const viewBoxSize = Math.max(gridWidth, gridHeight) * (1 + paddingRatio * 2)
  const gridLeft = (viewBoxSize - gridWidth) / 2
  const gridTop = (viewBoxSize - gridHeight) / 2
  const color = tintColor(icon.tint)
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
          fill={color}
        >
          <TextureMarks texture={style.texture} period={texturePeriod} />
        </pattern>
      </defs>
      <path d={cellsPath(false)} fill={color} />
      <path d={cellsPath(true)} fill={`url(#${textureId})`} />
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
