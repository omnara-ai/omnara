import React from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { theme } from '@/constants/theme';
import { formatDistanceToNow } from 'date-fns';
import Markdown from 'react-native-markdown-display';
import { User, Bot, MessageSquare } from 'lucide-react-native';
import { Message } from '@/types';
import { scrubQuestionFormatMarkers } from '@/utils/questionScrubber';
import * as Clipboard from 'expo-clipboard';

export interface ChatMessageData extends Message {}

export interface MessageGroup {
  id: string;
  agentName: string;
  timestamp: string;
  messages: ChatMessageData[];
  isFromAgent: boolean;
}

interface ChatMessageProps {
  messageGroup: MessageGroup;
  showWaitingIndicator?: boolean;
}

interface SingleMessageProps {
  message: ChatMessageData;
  isFirst: boolean;
  isLast: boolean;
  isOnly: boolean;
  showWaitingIndicator?: boolean;
}

// Markdown styling
const markdownStyles = {
  body: {
    color: theme.colors.text,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    lineHeight: theme.fontSize.sm * 1.5,
    marginTop: -8,
    marginBottom: -8,
  },
  paragraph: {
    marginTop: 0,
    marginBottom: 0,
    lineHeight: theme.fontSize.sm * 1.5,
  },
  heading1: {
    fontSize: theme.fontSize['2xl'],
    lineHeight: theme.fontSize['2xl'] * 1.3,
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    marginVertical: theme.spacing.sm,
  },
  heading2: {
    fontSize: theme.fontSize.xl,
    lineHeight: theme.fontSize.xl * 1.3,
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    marginVertical: theme.spacing.sm,
  },
  heading3: {
    fontSize: theme.fontSize.lg,
    lineHeight: theme.fontSize.lg * 1.3,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    marginVertical: theme.spacing.xs,
  },
  strong: {
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
  },
  em: {
    fontStyle: 'italic',
  },
  link: {
    color: theme.colors.primary,
    textDecorationLine: 'underline',
  },
  list_item: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginVertical: theme.spacing.xs / 2,
  },
  bullet_list: {
    marginTop: theme.spacing.xs,
    marginBottom: theme.spacing.xs,
  },
  ordered_list: {
    marginTop: theme.spacing.xs,
    marginBottom: theme.spacing.xs,
  },
  code_inline: {
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderRadius: 4,
    paddingHorizontal: 6,
    paddingVertical: 2,
    fontFamily: 'Menlo',
    fontSize: theme.fontSize.xs,
  },
  code_block: {
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    borderRadius: theme.borderRadius.md,
    padding: theme.spacing.md,
    marginVertical: theme.spacing.sm,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
  },
  fence: {
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    borderRadius: theme.borderRadius.md,
    padding: theme.spacing.md,
    marginVertical: theme.spacing.sm,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
  },
  blockquote: {
    borderLeftWidth: 3,
    borderLeftColor: theme.colors.primary,
    paddingLeft: theme.spacing.md,
    marginVertical: theme.spacing.sm,
    opacity: 0.8,
  },
  hr: {
    backgroundColor: 'rgba(255, 255, 255, 0.2)',
    height: 1,
    marginVertical: theme.spacing.md,
  },
};

const SingleMessage: React.FC<SingleMessageProps> = ({ 
  message, 
  isFirst, 
  isLast, 
  isOnly,
  showWaitingIndicator = false 
}) => {
  const getMessageStyle = () => {
    const baseStyle = [styles.messageBubble];
    
    // Glassmorphism effect
    if (message.sender_type === 'USER') {
      baseStyle.push(styles.userMessage);
    }
    
    // Highlight if waiting for input
    if (message.requires_user_input && message.sender_type === 'AGENT') {
      baseStyle.push(styles.waitingForInput);
    }
    
    // Connected bubbles styling
    if (isOnly) {
      baseStyle.push(styles.singleBubble);
    } else if (isFirst) {
      baseStyle.push(styles.firstBubble);
    } else if (isLast) {
      baseStyle.push(styles.lastBubble);
    } else {
      baseStyle.push(styles.middleBubble);
    }
    
    return baseStyle;
  };

  // Scrub options from agent messages before display
  const displayContent = message.sender_type === 'AGENT' 
    ? scrubQuestionFormatMarkers(message.content)
    : message.content;

  const handleCopyMessage = async () => {
    await Clipboard.setStringAsync(displayContent);
    Alert.alert('Copied', 'Message copied to clipboard', [{ text: 'OK' }]);
  };

  return (
    <>
      <TouchableOpacity 
        style={getMessageStyle()} 
        onLongPress={handleCopyMessage}
        activeOpacity={0.9}
      >
        <Markdown style={markdownStyles}>
          {displayContent}
        </Markdown>
      </TouchableOpacity>
      {showWaitingIndicator && (
        <View style={styles.waitingIndicator}>
          <MessageSquare size={16} color={theme.colors.warning} />
          <Text style={styles.waitingText}>Waiting for your response...</Text>
        </View>
      )}
    </>
  );
};

export const ChatMessage: React.FC<ChatMessageProps> = ({ messageGroup, showWaitingIndicator = false }) => {
  const timeAgo = formatDistanceToNow(new Date(messageGroup.timestamp), { addSuffix: true });
  
  const lastMessage = messageGroup.messages[messageGroup.messages.length - 1];
  const shouldShowWaiting = showWaitingIndicator && 
                           lastMessage?.requires_user_input && 
                           lastMessage?.sender_type === 'AGENT';

  return (
    <View style={styles.messageGroup}>
      {/* Header with Avatar */}
      <View style={styles.messageHeader}>
        <View style={styles.avatar}>
          {messageGroup.isFromAgent ? (
            <Bot size={24} color={theme.colors.primary} />
          ) : (
            <User size={24} color={theme.colors.white} />
          )}
        </View>
        <Text
          style={styles.senderName}
          numberOfLines={1}
          ellipsizeMode="tail"
        >
          {messageGroup.agentName}
        </Text>
        <Text style={styles.timestamp}>{timeAgo}</Text>
      </View>

      {/* Messages below header */}
      <View style={styles.messagesContainer}>
        {messageGroup.messages.map((message, index) => {
          const isFirst = index === 0 && messageGroup.messages.length > 1;
          const isLast = index === messageGroup.messages.length - 1 && messageGroup.messages.length > 1;
          const isOnly = messageGroup.messages.length === 1;
          const isLastMessageInGroup = shouldShowWaiting && index === messageGroup.messages.length - 1;
          
          return (
            <SingleMessage
              key={message.id}
              message={message}
              isFirst={isFirst}
              isLast={isLast}
              isOnly={isOnly}
              showWaitingIndicator={isLastMessageInGroup}
            />
          );
        })}
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  messageGroup: {
    marginBottom: theme.spacing.lg,
    paddingHorizontal: theme.spacing.md,
  },
  messageHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: theme.spacing.sm,
    height: 32,
  },
  avatar: {
    width: 32,
    height: 32,
    borderRadius: 16,
    backgroundColor: theme.colors.cardSurface,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: theme.spacing.sm,
  },
  messagesContainer: {
    // Messages are now below the header
  },
  senderName: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.primary,
    marginRight: theme.spacing.sm,
    flexShrink: 1,
    maxWidth: '55%',
  },
  timestamp: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  messageBubble: {
    backgroundColor: theme.colors.cardSurface,
    borderWidth: 1,
    borderColor: theme.colors.border,
    padding: theme.spacing.md,
    overflow: 'hidden',
  },
  userMessage: {
    backgroundColor: theme.colors.panelSurface,
    borderColor: theme.colors.borderLight,
    marginLeft: 40, // Indent user messages
  },
  waitingForInput: {
    backgroundColor: theme.colors.status.awaiting_input.bg,
    borderColor: theme.colors.status.awaiting_input.border,
  },
  singleBubble: {
    borderRadius: theme.borderRadius.lg,
    marginBottom: theme.spacing.xs,
  },
  firstBubble: {
    borderTopLeftRadius: theme.borderRadius.lg,
    borderTopRightRadius: theme.borderRadius.lg,
    borderBottomLeftRadius: 0,
    borderBottomRightRadius: 0,
    marginBottom: 0,
  },
  lastBubble: {
    borderTopLeftRadius: 0,
    borderTopRightRadius: 0,
    borderBottomLeftRadius: theme.borderRadius.lg,
    borderBottomRightRadius: theme.borderRadius.lg,
    marginBottom: theme.spacing.xs,
  },
  middleBubble: {
    borderRadius: 0,
    marginBottom: 0,
  },
  waitingIndicator: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: theme.colors.status.awaiting_input.bg,
    borderWidth: 1,
    borderColor: theme.colors.status.awaiting_input.border,
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.sm,
    marginTop: theme.spacing.sm,
  },
  waitingText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.warning,
    marginLeft: theme.spacing.xs,
  },
});
