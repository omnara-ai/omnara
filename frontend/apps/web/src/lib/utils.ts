import { type ClassValue, clsx } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

const twMerge = extendTailwindMerge({
  extend: {
    theme: { borderRadius: ['control', 'card'] },
    classGroups: {
      'font-size': [{ text: ['caption'] }],
      'font-weight': [{ font: ['heading'] }],
      leading: [{ leading: ['prose', 'control'] }],
      tracking: [{ tracking: ['eyebrow'] }],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
