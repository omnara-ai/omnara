import {
  type DiagramEdge,
  type DiagramNode,
  type Example,
  examples,
  type GroupNode,
} from '@/components/overview/agentSystemExamples'

export const CHIP_H = 26
export const CHIP_GAP = 6
export const CHIP_PAD_X = 9
export const GROUP_PAD = 10
const LABEL_H = 18
const SOLO_H = 38
export const SOLO_PAD_X = 12
const COL_GAP = 56
const ROW_GAP = 28
export const CALL_LINE_H = 20
export const CALL_PAD_X = 12
export const CALL_PAD_Y = 7
/** Height of the tag naming an action, which sits above its box. */
export const CALL_TAG_H = 18
export const ICON_SIZE = 14
/** Space an icon takes before its text. */
export const ICON_W = ICON_SIZE + 6
/** Average advances of the faces, for sizing chips, labels and the calls panel. */
const CHIP_CHAR_W = 6.6
const LABEL_CHAR_W = 7.6
export const CALL_CHAR_W = 7.5
/** How far a moving dot runs on under the cards at each end of a link. */
const DOT_RUN_ON = 28
/** Diagrams render slightly above 1:1 so labels read at a glance. */
export const SCALE = 1.1
/** How far a link looping back up a stacked diagram clears the widest card. */
export const LOOP_OUT = 28

/** A link as drawn; on small screens one running back up the stack loops round the right. */
export type DrawnEdge = DiagramEdge & { loop?: boolean }

export interface DrawnExample extends Omit<Example, 'edges'> {
  edges: DrawnEdge[]
}

/**
 * The example stacked in one column, in the order its nodes are listed, for screens
 * too narrow for the grid. Links back to an earlier node loop round the side.
 */
export function stacked(example: Example): DrawnExample {
  const order = new Map(example.nodes.map((node, index) => [node.id, index]))
  return {
    ...example,
    nodes: example.nodes.map((node, row) =>
      isGroup(node) ? { ...node, col: 0, row } : { ...node, col: 0, row, dy: 0 },
    ),
    edges: example.edges.map((edge) => ({
      ...edge,
      loop: (order.get(edge.to) ?? 0) < (order.get(edge.from) ?? 0),
    })),
  }
}

export interface PlacedNode {
  node: DiagramNode
  x: number
  y: number
  w: number
  h: number
}

export function isGroup(node: DiagramNode): node is GroupNode {
  return 'chips' in node
}

/** Groups of icon-only chips lay them out as a row of tiles, the rest stack labelled chips. */
export function isTileRow(group: GroupNode) {
  return group.chips.every((chip) => chip.short === undefined)
}

/** A labelled group holding one thing, drawn as that one thing. */
export function isSolo(group: GroupNode) {
  return group.label !== undefined && group.chips.length === 1
}

function nodeSize(node: DiagramNode) {
  if (isGroup(node) && isSolo(node)) {
    return {
      w: (node.label ?? '').length * CHIP_CHAR_W + ICON_W + SOLO_PAD_X * 2,
      h: SOLO_H,
    }
  }
  if (isGroup(node)) {
    const labelW = (node.label ?? 'Omnara').length * LABEL_CHAR_W + ICON_W
    if (isTileRow(node)) {
      const rowW = node.chips.length * CHIP_H + (node.chips.length - 1) * CHIP_GAP
      return {
        w: Math.max(rowW, labelW) + GROUP_PAD * 2,
        h: GROUP_PAD * 2 + LABEL_H + CHIP_H,
      }
    }
    const chipW = Math.max(...node.chips.map((chip) => (chip.short ?? '').length)) * CHIP_CHAR_W
    return {
      w: Math.max(chipW + ICON_W + CHIP_PAD_X * 2, labelW) + GROUP_PAD * 2,
      h: GROUP_PAD * 2 + LABEL_H + node.chips.length * CHIP_H + (node.chips.length - 1) * CHIP_GAP,
    }
  }
  return {
    w: Math.max(
      node.title.length * LABEL_CHAR_W,
      Math.max(...node.calls.map((call) => call.length)) * CALL_CHAR_W + CALL_PAD_X * 2,
    ),
    h: CALL_TAG_H + node.calls.length * CALL_LINE_H + CALL_PAD_Y * 2,
  }
}

/** Offsets of each track, from the widest or tallest node in it. */
function tracks(sizes: number[][], gap: number) {
  const extents = sizes.map((track) => Math.max(0, ...track))
  const starts = extents.map((_, index) =>
    extents.slice(0, index).reduce((sum, extent) => sum + extent + gap, 0),
  )
  const total = extents.reduce((sum, extent) => sum + extent, 0) + gap * (extents.length - 1)
  return { extents, starts, total }
}

/** Places each node centred in its grid cell. */
export function layout(example: DrawnExample) {
  const sized = example.nodes.map((node) => ({ node, ...nodeSize(node) }))
  const colCount = Math.max(...example.nodes.map((node) => node.col)) + 1
  const rowCount = Math.max(...example.nodes.map((node) => node.row)) + 1
  const cols = tracks(
    Array.from({ length: colCount }, (_, col) =>
      sized.filter(({ node }) => node.col === col).map(({ w }) => w),
    ),
    COL_GAP,
  )
  const rows = tracks(
    Array.from({ length: rowCount }, (_, row) =>
      sized.filter(({ node }) => node.row === row).map(({ h }) => h),
    ),
    ROW_GAP,
  )
  const placed = new Map<string, PlacedNode>(
    sized.map(({ node, w, h }) => [
      node.id,
      {
        node,
        w,
        h,
        x: (cols.starts[node.col] ?? 0) + ((cols.extents[node.col] ?? 0) - w) / 2,
        y:
          (rows.starts[node.row] ?? 0) +
          ((rows.extents[node.row] ?? 0) - h) / 2 +
          (isGroup(node) ? 0 : (node.dy ?? 0)),
      },
    ]),
  )
  const boxes = [...placed.values()]
  const top = Math.min(...boxes.map((box) => box.y))
  const bottom = Math.max(...boxes.map((box) => box.y + box.h))
  return { placed, width: cols.total, top, height: bottom - top }
}

/** Every example is drawn at the tallest one's height, so the card never resizes. */
export const DIAGRAM_H = Math.max(...examples.map((example) => layout(example).height))
/** The widest example's width as a grid, drawn at full scale. */
export const GRID_W = Math.max(...examples.map((example) => layout(example).width + 24)) * SCALE
/** The furthest the grid shrinks to fit its card before it stacks instead. */
export const MIN_GRID_SCALE = 0.75
export const STACKED_H = Math.max(...examples.map((example) => layout(stacked(example)).height))

export function chipTop(y: number, index: number) {
  return y + GROUP_PAD + LABEL_H + index * (CHIP_H + CHIP_GAP)
}

function chipCenterY(placed: PlacedNode, chip: number | undefined) {
  const { node } = placed
  if (!isGroup(node)) return placed.y + (CALL_TAG_H + placed.h) / 2
  if (chip === undefined || isSolo(node)) return placed.y + placed.h / 2
  return chipTop(placed.y, isTileRow(node) ? 0 : chip) + CHIP_H / 2
}

/**
 * Links nodes in one column top to bottom, and nodes in different columns side to
 * side, as a curve that leaves and arrives level. Also returns the path the moving
 * dot follows, which runs on under both cards so it slides out of and into them.
 * A looping link leaves and arrives on the right, bowing out past `loopX`.
 */
export function edgePaths(from: PlacedNode, to: PlacedNode, edge: DrawnEdge, loopX: number) {
  const n = String
  if (edge.loop) {
    const x1 = from.x + from.w
    const x2 = to.x + to.w
    const y1 = chipCenterY(from, edge.fromChip)
    const y2 = chipCenterY(to, edge.toChip)
    const curve = `C${n(loopX)} ${n(y1)} ${n(loopX)} ${n(y2)} ${n(x2)} ${n(y2)}`
    return {
      line: `M${n(x1)} ${n(y1)} ${curve}`,
      motion: `M${n(x1 - DOT_RUN_ON)} ${n(y1)} L${n(x1)} ${n(y1)} ${curve} L${n(x2 - DOT_RUN_ON)} ${n(y2)}`,
    }
  }
  if (from.node.col === to.node.col) {
    const dir = to.y > from.y ? 1 : -1
    const x1 = from.x + from.w / 2
    const x2 = to.x + to.w / 2
    const y1 = dir > 0 ? from.y + from.h : from.y
    const y2 = dir > 0 ? to.y : to.y + to.h
    const bend = (y2 - y1) / 2
    const curve = `C${n(x1)} ${n(y1 + bend)} ${n(x2)} ${n(y2 - bend)} ${n(x2)} ${n(y2)}`
    return {
      line: `M${n(x1)} ${n(y1)} ${curve}`,
      motion: `M${n(x1)} ${n(y1 - dir * DOT_RUN_ON)} L${n(x1)} ${n(y1)} ${curve} L${n(x2)} ${n(y2 + dir * DOT_RUN_ON)}`,
    }
  }
  const dir = to.node.col > from.node.col ? 1 : -1
  const x1 = dir > 0 ? from.x + from.w : from.x
  const x2 = dir > 0 ? to.x : to.x + to.w
  const y1 = chipCenterY(from, edge.fromChip) + (edge.fromOffset ?? 0)
  const y2 = chipCenterY(to, edge.toChip) + (edge.toOffset ?? 0)
  const bend = (x2 - x1) / 2
  const curve = `C${n(x1 + bend)} ${n(y1)} ${n(x2 - bend)} ${n(y2)} ${n(x2)} ${n(y2)}`
  return {
    line: `M${n(x1)} ${n(y1)} ${curve}`,
    motion: `M${n(x1 - dir * DOT_RUN_ON)} ${n(y1)} L${n(x1)} ${n(y1)} ${curve} L${n(x2 + dir * DOT_RUN_ON)} ${n(y2)}`,
  }
}
