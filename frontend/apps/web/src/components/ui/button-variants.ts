import { cva } from 'class-variance-authority'

export const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap type-control control-focus control-transition rounded-control border border-transparent disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4.5",
  {
    variants: {
      variant: {
        default:
          'bg-primary text-primary-foreground hover:bg-(--primary-hover) active:bg-(--primary-active)',
        destructive: 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
        outline: 'border-input bg-background hover:bg-accent hover:text-accent-foreground',
        secondary:
          'border-border bg-secondary text-secondary-foreground hover:bg-(--secondary-hover)',
        ghost: 'hover:bg-accent hover:text-accent-foreground',
        link: 'text-link hover:text-link-hover underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-10 px-5 py-2 has-[>svg]:px-3 data-[has-icon]:px-3',
        sm: 'h-9 gap-1.5 px-3 has-[>svg]:px-2.5 data-[has-icon]:px-2.5',
        lg: 'h-11 px-7 has-[>svg]:px-4 data-[has-icon]:px-4 sm:px-8',
        icon: 'size-10',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)
