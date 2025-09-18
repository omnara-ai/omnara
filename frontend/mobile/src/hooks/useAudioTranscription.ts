import { useState, useEffect, useRef } from 'react';
import { Alert, Platform } from 'react-native';
import { reportError } from '@/lib/sentry';
import {
  ExpoSpeechRecognitionModule,
  useSpeechRecognitionEvent,
} from 'expo-speech-recognition';

interface TranscriptionConfig {
  language?: string;
}

export const useAudioTranscription = (config?: TranscriptionConfig) => {
  const [isTranscribing, setIsTranscribing] = useState(false);
  const [transcriptionError, setTranscriptionError] = useState<string | null>(null);
  const [isAvailable, setIsAvailable] = useState(false);
  const [volumeLevel, setVolumeLevel] = useState(0);
  const transcribedTextRef = useRef<string>('');
  const recognitionRef = useRef<any>(null);

  // Check if native speech recognition is available
  useEffect(() => {
    const checkAvailability = async () => {
      try {
        const available = await ExpoSpeechRecognitionModule.isRecognitionAvailable();
        setIsAvailable(available);
      } catch (error) {
        setIsAvailable(false);
      }
    };
    checkAvailability();
  }, []);

  // Callbacks for speech events
  const [onSpeechEnd, setOnSpeechEnd] = useState<(() => void) | null>(null);
  const [onSpeechResult, setOnSpeechResult] = useState<((text: string) => void) | null>(null);
  const [onVolumeChange, setOnVolumeChange] = useState<((volume: number) => void) | null>(null);

  // Handle speech recognition events
  useSpeechRecognitionEvent('result', (event) => {
    const transcript = event.results[0]?.transcript || '';
    transcribedTextRef.current = transcript;

    // Call result callback if provided
    if (onSpeechResult) {
      onSpeechResult(transcript);
    }
  });

  useSpeechRecognitionEvent('end', () => {
    setIsTranscribing(false);
    setVolumeLevel(0);

    // Call end callback if provided
    if (onSpeechEnd) {
      onSpeechEnd();
      setOnSpeechEnd(null);
    }
  });

  useSpeechRecognitionEvent('volumechange', (event) => {
    // Volume is between -2 (quiet) and 10 (loud)
    // Normalize to 0-1 range
    const normalizedVolume = Math.max(0, Math.min(1, (event.value + 2) / 12));
    setVolumeLevel(normalizedVolume);

    if (onVolumeChange) {
      onVolumeChange(normalizedVolume);
    }
  });

  useSpeechRecognitionEvent('error', (event) => {
    setTranscriptionError(event.error);
    setIsTranscribing(false);
    setVolumeLevel(0);
    reportError(new Error(event.error), {
      context: 'Native speech recognition error',
      tags: { feature: 'voice-input-native' },
    });
  });

  // Start native speech recognition with auto-stop
  const startNativeRecognition = async (options?: {
    onEnd?: () => void;
    onResult?: (text: string) => void;
    onVolumeChange?: (volume: number) => void;
    autoStop?: boolean;
  }): Promise<void> => {
    try {
      const { granted } = await ExpoSpeechRecognitionModule.requestPermissionsAsync();
      if (!granted) {
        Alert.alert(
          'Permission Required',
          'Please enable microphone and speech recognition access to use voice input.',
        );
        return;
      }

      setIsTranscribing(true);
      setTranscriptionError(null);
      transcribedTextRef.current = '';

      // Set up callbacks
      if (options?.onEnd) {
        setOnSpeechEnd(() => options.onEnd);
      }
      if (options?.onResult) {
        setOnSpeechResult(() => options.onResult);
      }
      if (options?.onVolumeChange) {
        setOnVolumeChange(() => options.onVolumeChange);
      }

      const recognitionOptions = {
        lang: config?.language || 'en-US',
        interimResults: true, // This enables live transcription
        maxAlternatives: 1,
        continuous: false, // This makes it stop automatically when speech ends
        // Don't force on-device - let iOS use server for better accuracy
        // It will automatically fall back to on-device if offline
      };

      await ExpoSpeechRecognitionModule.start(recognitionOptions);
      recognitionRef.current = true;
    } catch (error) {
      setIsTranscribing(false);
      reportError(error, {
        context: 'Failed to start native speech recognition',
        tags: { feature: 'voice-input-native' },
      });
    }
  };

  // Stop native speech recognition
  const stopNativeRecognition = async (): Promise<string | null> => {
    try {
      if (recognitionRef.current) {
        await ExpoSpeechRecognitionModule.stop();
        recognitionRef.current = false;
      }
      return transcribedTextRef.current || null;
    } catch (error) {
      reportError(error, {
        context: 'Failed to stop native speech recognition',
        tags: { feature: 'voice-input-native' },
      });
      return null;
    }
  };


  return {
    // Native speech recognition
    isNativeAvailable: isAvailable,
    startNativeRecognition,
    stopNativeRecognition,

    // Status
    isTranscribing,
    transcriptionError,
    volumeLevel,
  };
};