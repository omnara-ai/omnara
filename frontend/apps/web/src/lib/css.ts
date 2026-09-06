import type { CSSProperties } from 'react'

export type CssVariables = CSSProperties & Record<`--${string}`, string | number | undefined>
