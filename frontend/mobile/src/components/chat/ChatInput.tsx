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
  Alert,
  Animated,
} from 'react-native';
import * as Haptics from 'expo-haptics';
import { theme } from '@/constants/theme';
import { Message } from '@/types';
import { StructuredQuestion, StructuredQuestionRef } from '@/components/ui/StructuredQuestion';
import { parseQuestionFormat } from '@/utils/questionParser';
import { Mic, ArrowUp, Square } from 'lucide-react-native';
import { useAudioTranscription } from '@/hooks/useAudioTranscription';
import { VoiceInputVisualizer } from '@/components/ui/VoiceInputVisualizer';
import { reportError } from '@/lib/logger';

interface ChatInputProps {
  isWaitingForInput: boolean;
  currentWaitingMessage: Message | null;
  onMessageSubmit: (content: string) => Promise<void>;
  isSubmitting: boolean;
  hasGitDiff?: boolean;
  canWrite?: boolean;
}

export const ChatInput: React.FC<ChatInputProps> = ({
  isWaitingForInput,
  currentWaitingMessage,
  onMessageSubmit,
  isSubmitting,
  hasGitDiff = false,
  canWrite = true,
}) => {
  const [message, setMessage] = useState('');
  const [isKeyboardVisible, setIsKeyboardVisible] = useState(false);
  const [isRecording, setIsRecording] = useState(false);
  const [recordingDuration, setRecordingDuration] = useState(0);
  const [liveTranscript, setLiveTranscript] = useState('');
  const [currentVolume, setCurrentVolume] = useState(0);
  const textInputRef = useRef<TextInput>(null);
  const questionRef = useRef<StructuredQuestionRef>(null);
  const durationTimer = useRef<NodeJS.Timeout | null>(null);
  const liveTranscriptRef = useRef<string>('');
  const {
    isNativeAvailable,
    startNativeRecognition,
    stopNativeRecognition,
    isTranscribing,
    volumeLevel
  } = useAudioTranscription();

  // Check if the current message has structured components
  const hasStructuredComponents = currentWaitingMessage ? 
    parseQuestionFormat(currentWaitingMessage.content).format !== 'open-ended' : false;
  const shouldShowStructuredHelper = canWrite && hasStructuredComponents;
  const showStructuredHelper = currentWaitingMessage && !isKeyboardVisible && shouldShowStructuredHelper;

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
      if (durationTimer.current) {
        clearInterval(durationTimer.current);
      }
    };
  }, []);


  const handleSpeechEnd = async () => {
    setIsRecording(false);

    if (durationTimer.current) {
      clearInterval(durationTimer.current);
      durationTimer.current = null;
    }

    if (liveTranscriptRef.current) {
      await Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
      setMessage(liveTranscriptRef.current);
    }

    setLiveTranscript('');
    liveTranscriptRef.current = '';
    setRecordingDuration(0);
  };

  const handleSpeechResult = (text: string) => {
    setLiveTranscript(text);
    liveTranscriptRef.current = text;
  };

  const handleVolumeChange = (volume: number) => {
    setCurrentVolume(volume);
  };

  const startRecording = async () => {
    try {
      if (!isNativeAvailable) {
        Alert.alert(
          'Voice Input Unavailable',
          'Speech recognition is not available on this device. Please type your message instead.',
        );
        return;
      }

      await Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Medium);

      setIsRecording(true);
      setRecordingDuration(0);
      setLiveTranscript('');
      liveTranscriptRef.current = '';

      await startNativeRecognition({
        onEnd: handleSpeechEnd,
        onResult: handleSpeechResult,
        onVolumeChange: handleVolumeChange,
        autoStop: true
      });

      durationTimer.current = setInterval(() => {
        setRecordingDuration(prev => prev + 1);
      }, 1000);
    } catch (error) {
      reportError(error, {
        context: 'Failed to start audio recording',
        tags: { feature: 'voice-input' },
      });
      Alert.alert('Recording Error', 'Unable to start recording. Please try again.');
    }
  };

  const stopRecording = async () => {
    if (!isRecording) return;

    try {
      await Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
      setIsRecording(false);

      if (durationTimer.current) {
        clearInterval(durationTimer.current);
        durationTimer.current = null;
      }

      await stopNativeRecognition();

      if (liveTranscriptRef.current) {
        setMessage(liveTranscriptRef.current);
      }

      setLiveTranscript('');
      liveTranscriptRef.current = '';
      setRecordingDuration(0);
    } catch (error) {
      setIsRecording(false);
      setLiveTranscript('');
      reportError(error, {
        context: 'Failed to stop speech recognition',
        tags: { feature: 'voice-input' },
      });
      Alert.alert('Voice Input Error', 'Unable to process speech. Please try again.');
    }
  };


  const handleSubmit = async () => {
    if (!canWrite || !message.trim() || isSubmitting) return;

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

  const handleMicrophonePress = () => {
    if (isRecording) {
      stopRecording();
    } else {
      startRecording();
    }
  };

  const formatDuration = (seconds: number) => {
    const mins = Math.floor(seconds / 60);
    const secs = seconds % 60;
    return `${mins}:${secs.toString().padStart(2, '0')}`;
  };

  const handleStructuredAnswer = (answer: string) => {
    setMessage(answer);
  };

  const handleAnswerAndSubmit = async (answer: string) => {
    if (!canWrite) {
      return;
    }
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

  const placeholder = !canWrite
    ? 'You have read-only access to this chat.'
    : isWaitingForInput
      ? 'Type your response...'
      : 'Share your thoughts...';

  return (
    <View style={[
      styles.container,
      // Add top margin when no structured components will show
      (!currentWaitingMessage || !shouldShowStructuredHelper) && !hasGitDiff ? styles.containerWithTopMargin : null
    ]}>
      {/* Structured Question Helper - only show when keyboard is hidden AND has structured components */}
      {showStructuredHelper && (
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
      
      <Animated.View style={[
        styles.inputContainer,
        isRecording && styles.inputContainerRecording
      ]}>
        {isRecording ? (
          <VoiceInputVisualizer
            volumeLevel={currentVolume}
            isRecording={isRecording}
            liveTranscript={liveTranscript}
            duration={recordingDuration}
          />
        ) : (
          <TextInput
            ref={textInputRef}
            style={styles.textInput}
            value={message}
            onChangeText={setMessage}
            placeholder={placeholder}
            placeholderTextColor={theme.colors.textMuted}
            multiline
            maxLength={100000}
            editable={canWrite && !isSubmitting && !isTranscribing}
            onSubmitEditing={handleSubmit}
            blurOnSubmit={false}
            autoCapitalize="none"
          />
        )}


        <TouchableOpacity
          style={[
            styles.sendButton,
            isRecording && styles.recordingButton,
            (!canWrite || (message.trim() === '' && !isRecording) || isSubmitting) && styles.sendButtonDisabled
          ]}
          onPress={message.trim() ? handleSubmit : handleMicrophonePress}
          disabled={!canWrite || isSubmitting}
          activeOpacity={0.7}
        >
          {isSubmitting ? (
            <ActivityIndicator size="small" color={theme.colors.white} />
          ) : isRecording ? (
            <Square
              size={18}
              color={theme.colors.white}
              fill={theme.colors.white}
            />
          ) : message.trim() ? (
            <ArrowUp
              size={20}
              color={theme.colors.text}
              strokeWidth={2.5}
            />
          ) : (
            <Mic
              size={20}
              color={theme.colors.text}
            />
          )}
        </TouchableOpacity>
      </Animated.View>
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
    backgroundColor: theme.colors.cardSurface,
    borderWidth: 1,
    borderColor: theme.colors.border,
    borderRadius: 22,
    paddingRight: theme.spacing.xs,
    minHeight: 42,
    maxHeight: 168, // Allow expansion for multi-line text
  },
  inputContainerRecording: {
    borderColor: theme.colors.primary,
    backgroundColor: theme.colors.glass.primary,
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
    color: theme.colors.text,
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
    borderColor: theme.colors.borderLight,
    marginBottom: 5,
  },
  sendButtonDisabled: {
    backgroundColor: theme.colors.glass.white,
    borderColor: theme.colors.borderDivider,
  },
  sendIcon: {
    fontSize: 18,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.text,
    marginTop: Platform.OS === 'ios' ? -2 : 0,
  },
  recordingButton: {
    backgroundColor: theme.colors.error,
  },
});
