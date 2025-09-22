import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { X, Terminal, Plus, Copy, Check } from 'lucide-react'
import { toast } from 'sonner'

interface OnboardingFlowSimpleProps {
  onComplete: () => void
}

export const OnboardingFlowSimple = ({ onComplete }: OnboardingFlowSimpleProps) => {
  const [copiedInstall, setCopiedInstall] = useState(false)
  const [copiedRun, setCopiedRun] = useState(false)
  const [copiedUvInstall, setCopiedUvInstall] = useState(false)
  const [copiedUvRun, setCopiedUvRun] = useState(false)
  const [showUv, setShowUv] = useState(false)

  const handleCopyInstall = async () => {
    const command = 'pip install omnara'
    try {
      await navigator.clipboard.writeText(command)
      setCopiedInstall(true)
      toast.success('Command copied to clipboard!')
      setTimeout(() => setCopiedInstall(false), 2000)
    } catch (err) {
      console.error('Failed to copy command:', err)
      toast.error('Failed to copy command. Please copy manually.')
    }
  }

  const handleCopyRun = async () => {
    const command = 'omnara'
    try {
      await navigator.clipboard.writeText(command)
      setCopiedRun(true)
      toast.success('Command copied to clipboard!')
      setTimeout(() => setCopiedRun(false), 2000)
    } catch (err) {
      console.error('Failed to copy command:', err)
      toast.error('Failed to copy command. Please copy manually.')
    }
  }

  const handleCopyUvInstall = async () => {
    const command = 'uv pip install omnara'
    try {
      await navigator.clipboard.writeText(command)
      setCopiedUvInstall(true)
      toast.success('Command copied to clipboard!')
      setTimeout(() => setCopiedUvInstall(false), 2000)
    } catch (err) {
      console.error('Failed to copy command:', err)
      toast.error('Failed to copy command. Please copy manually.')
    }
  }

  const handleCopyUvRun = async () => {
    const command = 'uv run omnara'
    try {
      await navigator.clipboard.writeText(command)
      setCopiedUvRun(true)
      toast.success('Command copied to clipboard!')
      setTimeout(() => setCopiedUvRun(false), 2000)
    } catch (err) {
      console.error('Failed to copy command:', err)
      toast.error('Failed to copy command. Please copy manually.')
    }
  }


  return (
    <div className="min-h-[80vh] flex items-center justify-center px-4">
      <div className="max-w-3xl w-full">
        {/* Skip Button */}
        <div className="flex justify-end mb-4">
          <Button
            variant="ghost"
            onClick={onComplete}
            className="text-off-white/60 hover:text-white hover:bg-white/10 transition-colors text-sm"
          >
            <X className="w-4 h-4 mr-1" />
            Skip
          </Button>
        </div>

        {/* Header */}
        <div className="text-center mb-8">
          <div className="flex justify-center mb-4">
            <Terminal size={40} className="text-cozy-amber" strokeWidth={1.5} />
          </div>
          <h1 className="text-3xl font-bold text-white mb-2">Get Started with Claude Code</h1>
          <p className="text-base text-off-white/80">
            Just two simple commands to get started:
          </p>
          {/* Package Manager Toggle */}
          <div className="mt-4 flex items-center justify-center space-x-4">
            <button
              onClick={() => setShowUv(false)}
              className={`px-4 py-2 rounded-lg transition-all ${
                !showUv 
                  ? 'bg-cozy-amber/20 text-cozy-amber border border-cozy-amber/30' 
                  : 'text-off-white/60 hover:text-off-white'
              }`}
            >
              pip
            </button>
            <button
              onClick={() => setShowUv(true)}
              className={`px-4 py-2 rounded-lg transition-all ${
                showUv 
                  ? 'bg-cozy-amber/20 text-cozy-amber border border-cozy-amber/30' 
                  : 'text-off-white/60 hover:text-off-white'
              }`}
            >
              uv
            </button>
          </div>
        </div>

        {/* 2-Step Compact Flow */}
        <div className="space-y-4 mb-8">
          {/* Step 1: Install Omnara */}
          <div className="border border-cozy-amber/20 bg-cozy-amber/5 rounded-lg p-4">
            <div className="flex items-start space-x-3">
              <div className="text-lg font-bold text-cozy-amber">1</div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-white mb-1">Install Omnara</h3>
                <div className="bg-gray-900/50 border border-off-white/10 rounded p-3 flex items-center justify-between">
                  <code className="text-cozy-amber font-mono text-sm">
                    {showUv ? 'uv pip install omnara' : 'pip install omnara'}
                  </code>
                  <Button
                    onClick={showUv ? handleCopyUvInstall : handleCopyInstall}
                    size="sm"
                    variant="ghost"
                    className={`ml-2 px-2 py-1 text-xs ${
                      (showUv ? copiedUvInstall : copiedInstall)
                        ? 'text-green-400 hover:text-green-400'
                        : 'text-off-white/60 hover:text-white'
                    }`}
                  >
                    {(showUv ? copiedUvInstall : copiedInstall) ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                  </Button>
                </div>
              </div>
            </div>
          </div>

          {/* Step 2: Run Omnara */}
          <div className="border border-cozy-amber/20 bg-cozy-amber/5 rounded-lg p-4">
            <div className="flex items-start space-x-3">
              <div className="text-lg font-bold text-cozy-amber">2</div>
              <div className="flex-1">
                <h3 className="text-lg font-semibold text-white mb-1">Run in your project</h3>
                <div className="bg-gray-900/50 border border-off-white/10 rounded p-3 flex items-center justify-between">
                  <code className="text-cozy-amber font-mono text-sm">
                    {showUv ? 'uv run omnara' : 'omnara'}
                  </code>
                  <Button
                    onClick={showUv ? handleCopyUvRun : handleCopyRun}
                    size="sm"
                    variant="ghost"
                    className={`ml-2 px-2 py-1 text-xs ${
                      (showUv ? copiedUvRun : copiedRun)
                        ? 'text-green-400 hover:text-green-400'
                        : 'text-off-white/60 hover:text-white'
                    }`}
                  >
                    {(showUv ? copiedUvRun : copiedRun) ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </div>

        {/* That's it note */}
        <div className="text-center mb-8">
          <p className="text-off-white/60 text-sm mb-4">
            ✨ That's it! You can now use Claude Code through the CLI, or connect from anywhere using the Omnara mobile app or web dashboard.
          </p>
        </div>

        {/* Continue to Dashboard button */}
        <div className="flex justify-center">
          <Button 
            onClick={onComplete}
            size="lg"
            className="px-6 py-2 bg-cozy-amber text-warm-charcoal hover:bg-cozy-amber/90 transition-colors"
          >
            Continue to Dashboard
          </Button>
        </div>
      </div>
    </div>
  )
}