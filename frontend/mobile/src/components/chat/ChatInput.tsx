import React, { useState, useRef, useEffect } from 'react';
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  StyleSheet,
  ActivityIndicator,
  Platform,
  Keyboard,
} from 'react-native';
import { theme } from '@/constants/theme';
import { Message } from '@/types';
import { StructuredQuestion, StructuredQuestionRef } from '@/components/ui/StructuredQuestion';
import { parseQuestionFormat } from '@/utils/questionParser';
import { reportError } from '@/lib/sentry';

interface ChatInputProps {
  isWaitingForInput: boolean;
  currentWaitingMessage: Message | null;
  onMessageSubmit: (content: string) => Promise<void>;
  isSubmitting: boolean;
  hasGitDiff?: boolean;
}

export const ChatInput: React.FC<ChatInputProps> = ({
  isWaitingForInput,
  currentWaitingMessage,
  onMessageSubmit,
  isSubmitting,
  hasGitDiff = false,
}) => {
  const [message, setMessage] = useState('');
  const [isKeyboardVisible, setIsKeyboardVisible] = useState(false);
  const textInputRef = useRef<TextInput>(null);
  const questionRef = useRef<StructuredQuestionRef>(null);

  // Check if the current message has structured components
  const hasStructuredComponents = currentWaitingMessage ? 
    parseQuestionFormat(currentWaitingMessage.content).format !== 'open-ended' : false;

  useEffect(() => {
    const keyboardWillShowListener = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow', 
      () => {
        setIsKeyboardVisible(true);
      }
    );
    const keyboardWillHideListener = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide',
      () => {
        setIsKeyboardVisible(false);
      }
    );

    return () => {
      keyboardWillShowListener.remove();
      keyboardWillHideListener.remove();
    };
  }, []);

  const handleSubmit = async () => {
    if (!message.trim() || isSubmitting) return;
    
    const messageToSend = message;
    setMessage(''); // Clear input immediately for better UX
    
    try {
      await onMessageSubmit(messageToSend);
    } catch (error) {
      // If submission fails, restore the message
      setMessage(messageToSend);
      reportError(error, {
        context: 'Failed to submit user message from ChatInput',
        tags: { feature: 'mobile-chat-input' },
      });
    }
  };

  const handleStructuredAnswer = (answer: string) => {
    setMessage(answer);
  };

  const handleAnswerAndSubmit = async (answer: string) => {
    setMessage(answer);
    // Clear input immediately for better UX
    setMessage('');
    
    try {
      await onMessageSubmit(answer);
    } catch (error) {
      // If submission fails, restore the message
      setMessage(answer);
      reportError(error, {
        context: 'Failed to submit structured answer',
        tags: { feature: 'mobile-chat-input' },
      });
    }
  };

  const handleFocusTextInput = () => {
    textInputRef.current?.focus();
  };

  const placeholder = isWaitingForInput 
    ? "Type your response..." 
    : "Share your thoughts...";

  return (
    <View style={[
      styles.container,
      // Add top margin when no structured components will show
      (!currentWaitingMessage || !hasStructuredComponents) && !hasGitDiff ? styles.containerWithTopMargin : null
    ]}>
      {/* Structured Question Helper - only show when keyboard is hidden AND has structured components */}
      {currentWaitingMessage && !isKeyboardVisible && hasStructuredComponents && (
        <View style={styles.structuredQuestionWrapper}>
          <StructuredQuestion
            ref={questionRef}
            questionText={currentWaitingMessage.content}
            onAnswer={handleStructuredAnswer}
            onAnswerAndSubmit={handleAnswerAndSubmit}
            onFocusTextInput={handleFocusTextInput}
          />
        </View>
      )}
      
      <View style={styles.inputContainer}>
        <TextInput
          ref={textInputRef}
          style={styles.textInput}
          value={message}
          onChangeText={setMessage}
          placeholder={placeholder}
          placeholderTextColor="rgba(255, 255, 255, 0.4)"
          multiline
          maxLength={100000}
          editable={!isSubmitting}
          onSubmitEditing={handleSubmit}
          blurOnSubmit={false}
        />
        
        <TouchableOpacity
          style={[
            styles.sendButton,
            (!message.trim() || isSubmitting) && styles.sendButtonDisabled
          ]}
          onPress={handleSubmit}
          disabled={!message.trim() || isSubmitting}
          activeOpacity={0.7}
        >
          {isSubmitting ? (
            <ActivityIndicator size="small" color={theme.colors.white} />
          ) : (
            <Text style={styles.sendIcon}>↑</Text>
          )}
        </TouchableOpacity>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    gap: theme.spacing.xs,
  },
  containerWithTopMargin: {
    marginTop: theme.spacing.sm,
  },
  structuredQuestionWrapper: {
    marginTop: theme.spacing.sm,
    marginBottom: theme.spacing.sm,
  },
  inputContainer: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    borderRadius: 22,
    paddingRight: theme.spacing.xs,
    minHeight: 42,
  },
  textInput: {
    flex: 1,
    backgroundColor: 'transparent',
    borderWidth: 0,
    paddingLeft: theme.spacing.md,
    paddingRight: theme.spacing.sm,
    paddingTop: 12,
    paddingBottom: 12,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    minHeight: 42,
    maxHeight: 168,
    textAlignVertical: 'center',
  },
  sendButton: {
    width: 32,
    height: 32,
    backgroundColor: theme.colors.primary,
    borderRadius: 16,
    justifyContent: 'center',
    alignItems: 'center',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.3)',
    marginBottom: 5,
  },
  sendButtonDisabled: {
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    borderColor: 'rgba(255, 255, 255, 0.1)',
  },
  sendIcon: {
    fontSize: 18,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginTop: Platform.OS === 'ios' ? -2 : 0,
  },
});
