import { cva } from 'class-variance-authority'

export const textFieldVariants = cva('', {
  variants: {
    variant: {
      default: 'control-focus rounded-control border border-input bg-card',
      // Embedded fields use their containing control or an inline underline for focus.
      embedded: 'border-0 bg-transparent shadow-none focus-visible:outline-none',
    },
  },
  defaultVariants: { variant: 'default' },
})
