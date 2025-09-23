
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Textarea } from '@/components/ui/textarea'
import { Send } from 'lucide-react'
import { AgentStatus } from '@/types/dashboard'

interface FeedbackFormProps {
  instanceStatus: AgentStatus
  instanceId: string
  onFeedbackSubmit: (feedback: string) => Promise<void>
}

export function FeedbackForm({ instanceStatus, instanceId, onFeedbackSubmit }: FeedbackFormProps) {
  const [feedback, setFeedback] = useState('')
  const [feedbackSubmitting, setFeedbackSubmitting] = useState(false)

  const handleFeedbackSubmit = async () => {
    if (!feedback.trim() || feedbackSubmitting) return
    
    setFeedbackSubmitting(true)
    try {
      await onFeedbackSubmit(feedback.trim())
      setFeedback('')
    } catch (err) {
      console.error('Failed to submit feedback:', err)
    } finally {
      setFeedbackSubmitting(false)
    }
  }

  if (instanceStatus !== AgentStatus.ACTIVE) {
    return null
  }

  return (
    <Card className="border border-blue-400/30 bg-blue-500/10 backdrop-blur-md">
      <CardHeader>
        <CardTitle className="flex items-center space-x-2 text-blue-200">
          <Send className="h-5 w-5" />
          <span>Send Feedback to Agent</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <Textarea
          value={feedback}
          onChange={(e) => setFeedback(e.target.value)}
          placeholder="Share context, guidance, or feedback with your agent..."
          className="min-h-[100px] bg-white/10 backdrop-blur-sm border-white/20 text-white placeholder:text-off-white/60 focus:ring-electric-accent focus:ring-offset-midnight-blue"
          disabled={feedbackSubmitting}
        />
        <Button
          onClick={handleFeedbackSubmit}
          disabled={!feedback.trim() || feedbackSubmitting}
          className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0"
        >
          {feedbackSubmitting ? 'Sending...' : 'Send Feedback'}
        </Button>
      </CardContent>
    </Card>
  )
}
