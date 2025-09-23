import { useState, useRef } from 'react'
import { Textarea } from '@/components/ui/textarea'
import { StructuredQuestion, StructuredQuestionRef } from './../questions/StructuredQuestion'
import { parseQuestionFormat } from '@/utils/questionParser'
import { Message } from '@/types/dashboard'

interface ChatInputProps {
  isWaitingForInput: boolean
  currentWaitingMessage: Message | null
  onMessageSubmit: (content: string) => Promise<void>
  isSubmitting: boolean
  hasGitDiff?: boolean
  canWrite: boolean
}

export function ChatInput({ 
  isWaitingForInput, 
  currentWaitingMessage,
  onMessageSubmit, 
  isSubmitting,
  hasGitDiff = false,
  canWrite,
}: ChatInputProps) {
  const [message, setMessage] = useState('')
  const textAreaRef = useRef<HTMLTextAreaElement>(null)
  const questionRef = useRef<StructuredQuestionRef>(null)
  
  // Check if the current message has structured components
  const hasStructuredComponents = currentWaitingMessage ? 
    parseQuestionFormat(currentWaitingMessage.content).format !== 'open-ended' : false

  const handleSubmit = async () => {
    if (!message.trim() || isSubmitting || !canWrite) return
    
    await onMessageSubmit(message)
    
    setMessage('')
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit()
    }
  }
  
  const handleStructuredAnswer = (answer: string) => {
    setMessage(answer)
  }
  
  const handleAnswerAndSubmit = async (answer: string) => {
    setMessage(answer)
    // Submit immediately
    await onMessageSubmit(answer)
    setMessage('')
  }
  
  const handleFocusTextInput = () => {
    textAreaRef.current?.focus()
  }

  return (
    <div className={`bg-white/10 backdrop-blur-md p-4 space-y-4 ${hasGitDiff ? 'rounded-b-xl' : 'rounded-xl'}`}>
      {/* Structured Question Helper - only show when has structured components */}
      {currentWaitingMessage && hasStructuredComponents && (
        <div className="mb-2">
          <StructuredQuestion
            ref={questionRef}
            questionText={currentWaitingMessage.content}
            onAnswer={handleStructuredAnswer}
            onAnswerAndSubmit={handleAnswerAndSubmit}
            onFocusTextInput={handleFocusTextInput}
          />
        </div>
      )}
      
      <div className="space-y-3">
        {/* Message Input */}
        <div className="relative flex items-center">
          <Textarea
            ref={textAreaRef}
            value={message}
            onChange={(e) => {
              setMessage(e.target.value)
              // Auto-resize textarea
              e.target.style.height = 'auto'
              e.target.style.height = Math.min(e.target.scrollHeight, 120) + 'px'
            }}
            onKeyDown={handleKeyDown}
            placeholder={
              canWrite
                ? isWaitingForInput
                  ? 'Type your response here...'
                  : 'Type your response here...'
                : 'You have read-only access to this chat'
            }
            className="resize-none bg-white/10 backdrop-blur-md border border-white/20 text-off-white placeholder:text-off-white/60 focus:border-electric-accent focus:outline-none shadow-lg rounded-xl textarea-custom-scrollbar text-base pr-12 px-4"
            disabled={isSubmitting || !canWrite}
            rows={1}
            style={{ 
              minHeight: '48px',
              maxHeight: '120px',
              paddingTop: '12px',
              paddingBottom: '12px',
              lineHeight: '24px',
              overflow: 'auto'
            }}
          />
          
          {/* Send button positioned inside textarea */}
          <button
            onClick={handleSubmit}
            disabled={!message.trim() || isSubmitting || !canWrite}
            className="absolute right-2 w-9 h-9 rounded-lg bg-black text-white flex items-center justify-center hover:bg-gray-800 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            aria-label="Send message"
          >
            <svg
              width="18"
              height="18"
              viewBox="0 0 18 18"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
              className="transform -rotate-90"
            >
              <path
                d="M3 9L15 9M15 9L11 5M15 9L11 13"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        </div>
      </div>

      <style dangerouslySetInnerHTML={{
        __html: `
          .textarea-custom-scrollbar::-webkit-scrollbar {
            width: 8px;
          }
          
          .textarea-custom-scrollbar::-webkit-scrollbar-track {
            background: transparent;
          }
          
          .textarea-custom-scrollbar::-webkit-scrollbar-thumb {
            background: transparent;
            border-radius: 10px;
            border: 3px solid transparent;
            background-clip: content-box;
          }
          
          .textarea-custom-scrollbar::-webkit-scrollbar-thumb:hover {
            background: transparent;
          }
        `
      }} />
    </div>
  )
}
