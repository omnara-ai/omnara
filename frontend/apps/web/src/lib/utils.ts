import { type ClassValue, clsx } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      text: ['caption'],
      'font-weight': ['heading'],
      leading: ['prose', 'control'],
      tracking: ['eyebrow'],
      radius: ['control', 'card'],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
