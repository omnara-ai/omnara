
import { useState } from 'react'
import { Button } from '../ui/button'
import { Copy, Check } from 'lucide-react'

interface CopyButtonProps {
  text: string
  label: string
  size?: 'sm' | 'xs'
  variant?: 'ghost' | 'outline'
}

export function CopyButton({ text, label, size = 'xs', variant = 'ghost' }: CopyButtonProps) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async (e: React.MouseEvent) => {
    e.stopPropagation() // Prevent row click when clicking copy button
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (err) {
      console.error('Failed to copy:', err)
    }
  }

  if (size === 'xs') {
    return (
      <button
        onClick={handleCopy}
        className="p-1 hover:bg-white/10 rounded transition-colors text-gray-400 hover:text-white"
        title={copied ? 'Copied!' : `Copy ${label || 'ID'}`}
      >
        {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
      </button>
    )
  }

  return (
    <Button
      size={size}
      variant={variant}
      onClick={handleCopy}
      className="bg-white/10 border-white/20 text-white hover:bg-white/20"
    >
      {copied ? <Check className="h-4 w-4 mr-2" /> : <Copy className="h-4 w-4 mr-2" />}
      {copied ? 'Copied!' : `Copy ${label}`}
    </Button>
  )
}
