import { Bot, User, MessageSquare } from 'lucide-react'
import { ChatMessageData, MessageGroup } from './ChatInterface'
import { formatDistanceToNow } from 'date-fns'
import ReactMarkdown from 'react-markdown'
import { preprocessMarkdown, markdownComponents, remarkPlugins } from '../markdownConfig'
import { scrubQuestionFormatMarkers } from '@/utils/questionScrubber'

interface ChatMessageProps {
  messageGroup: MessageGroup
  showWaitingIndicator?: boolean
}

function SingleMessage({ 
  message, 
  isFirst, 
  isLast, 
  isOnly,
  showWaitingIndicator = false
}: { 
  message: ChatMessageData; 
  isFirst: boolean; 
  isLast: boolean; 
  isOnly: boolean; 
  showWaitingIndicator?: boolean;
}) {

  const getMessageClasses = () => {
    // Base styling with enhanced glassmorphism (no borders)
    let baseClasses = 'backdrop-blur-lg p-6 break-words overflow-hidden transition-all duration-200'
    
    // Enhanced glassmorphism effect based on sender
    if (message.sender_type === 'USER') {
      // User messages: more prominent, right-aligned feel
      baseClasses += ' bg-blue-500/10 shadow-xl mr-8 max-w-4xl'
    } else {
      // Agent messages: subtle, professional, full width
      baseClasses += ' bg-surface-panel/80 shadow-lg w-full'
    }
    
    // Connected bubbles styling - enhanced rounded corners
    if (isOnly) {
      baseClasses += ' rounded-2xl mb-3'
    } else if (isFirst) {
      baseClasses += ' rounded-t-2xl rounded-b-md mb-1'
    } else if (isLast) {
      baseClasses += ' rounded-t-md rounded-b-2xl mb-3'
    } else {
      baseClasses += ' rounded-md mb-1'
    }
    
    // Special highlight for messages requiring input
    if (message.requires_user_input && message.sender_type === 'AGENT') {
      baseClasses += ' bg-yellow-400/5'
    }

    return baseClasses
  }

  // Scrub options from agent messages before display
  const displayContent = message.sender_type === 'AGENT' 
    ? scrubQuestionFormatMarkers(message.content)
    : message.content;

  // Check if this is a tool usage message
  const isToolMessage = displayContent.includes('Using tool:')

  return (
    <div className={getMessageClasses()} data-message-id={message.id}>
      <div className={`prose prose-invert prose-pre:whitespace-pre-wrap prose-pre:break-words max-w-full text-text-primary overflow-hidden prose-code:text-text-primary prose-code:bg-background-base prose-code:px-2 prose-code:py-1 prose-code:rounded-md ${isToolMessage ? 'bg-background-base rounded-lg p-3 mt-3' : ''}`}>
        <ReactMarkdown 
          components={markdownComponents}
          remarkPlugins={remarkPlugins}
        >
          {preprocessMarkdown(displayContent)}
        </ReactMarkdown>
      </div>
      {showWaitingIndicator && (
        <div className="bg-surface-panel/90 backdrop-blur-lg rounded-xl p-4 shadow-xl mt-4">
          <div className="text-sm text-yellow-400 font-medium flex items-center space-x-2">
            <MessageSquare className="h-4 w-4" />
            <span>Waiting for your response...</span>
          </div>
        </div>
      )}
    </div>
  )
}

export function ChatMessage({ messageGroup, showWaitingIndicator = false }: ChatMessageProps) {
  const timeAgo = formatDistanceToNow(new Date(messageGroup.timestamp), { addSuffix: true })

  const getMessageIcon = () => {
    if (messageGroup.isFromAgent) {
      return (
        <div className="w-8 h-8 bg-gradient-to-br from-surface-panel to-interactive-hover rounded-full flex items-center justify-center shadow-lg">
          <Bot className="h-5 w-5 text-text-primary" />
        </div>
      )
    } else {
      return (
        <div className="w-8 h-8 bg-gradient-to-br from-surface-panel to-interactive-hover rounded-full flex items-center justify-center shadow-lg">
          <User className="h-5 w-5 text-text-primary" />
        </div>
      )
    }
  }

  const getNameColor = () => {
    return messageGroup.isFromAgent ? 'text-text-primary' : 'text-blue-400'
  }

  // Determine if this is a user message group
  const isUserMessage = !messageGroup.isFromAgent

  return (
    <div className={`animate-fade-in mb-8 ${isUserMessage ? 'flex justify-end' : 'w-full'}`}>
      {!isUserMessage ? (
        // Agent messages: Full width layout with embedded avatar
        <div className="w-full">
          {/* Enhanced Header with avatar */}
          <div className="flex items-center space-x-3 mb-4 h-6">
            <div className="flex-shrink-0 flex items-center h-8">
              {getMessageIcon()}
            </div>
            <span className={`font-semibold text-lg ${getNameColor()}`}>
              {messageGroup.agentName}
            </span>
            <span className="text-xs text-text-secondary/80 bg-surface-panel/50 px-2 py-1 rounded-full">
              {timeAgo}
            </span>
          </div>
          
          {/* Messages in group */}
          <div className="w-full">
            {messageGroup.messages.map((message, index) => {
              const isFirst = index === 0 && messageGroup.messages.length > 1
              const isLast = index === messageGroup.messages.length - 1 && messageGroup.messages.length > 1
              const isOnly = messageGroup.messages.length === 1
              const isLastMessageInLastGroup = showWaitingIndicator && index === messageGroup.messages.length - 1
              
              return (
                <SingleMessage 
                  key={message.id}
                  message={message}
                  isFirst={isFirst}
                  isLast={isLast}
                  isOnly={isOnly}
                  showWaitingIndicator={isLastMessageInLastGroup}
                />
              )
            })}
          </div>
        </div>
      ) : (
        // User messages: Right-aligned layout (unchanged)
        <div className="flex flex-col items-end max-w-4xl">
          <div className="flex items-center space-x-3 mb-3 h-6 justify-end">
            <span className="text-xs text-text-secondary/80 bg-surface-panel/50 px-2 py-1 rounded-full">
              {timeAgo}
            </span>
            <span className="font-semibold text-sm text-text-primary">
              {messageGroup.agentName}
            </span>
            <div className="flex-shrink-0 flex items-center h-8">
              {getMessageIcon()}
            </div>
          </div>

          {/* Messages in group */}
          <div>
            {messageGroup.messages.map((message, index) => {
              const isFirst = index === 0 && messageGroup.messages.length > 1
              const isLast = index === messageGroup.messages.length - 1 && messageGroup.messages.length > 1
              const isOnly = messageGroup.messages.length === 1
              const isLastMessageInLastGroup = showWaitingIndicator && index === messageGroup.messages.length - 1
              
              return (
                <SingleMessage 
                  key={message.id}
                  message={message}
                  isFirst={isFirst}
                  isLast={isLast}
                  isOnly={isOnly}
                  showWaitingIndicator={isLastMessageInLastGroup}
                />
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
