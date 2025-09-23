import { useState, useEffect, useRef } from 'react';
import { Alert, Platform } from 'react-native';
import {
  ExpoSpeechRecognitionModule,
  useSpeechRecognitionEvent,
} from 'expo-speech-recognition';
import { reportError } from '@/lib/logger';

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

  useSpeechRecognitionEvent('result', (event) => {
    const transcript = event.results[0]?.transcript || '';
    transcribedTextRef.current = transcript;

    if (onSpeechResult) {
      onSpeechResult(transcript);
    }
  });

  useSpeechRecognitionEvent('end', () => {
    setIsTranscribing(false);
    setVolumeLevel(0);

    if (onSpeechEnd) {
      onSpeechEnd();
      setOnSpeechEnd(null);
    }
  });

  useSpeechRecognitionEvent('volumechange', (event) => {
    const normalizedVolume = Math.max(0, Math.min(1, (event.value + 2) / 12));
    setVolumeLevel(normalizedVolume);

    if (onVolumeChange) {
      onVolumeChange(normalizedVolume);
    }
  });

  useSpeechRecognitionEvent('error', (event) => {
    if (event.error === 'no-speech') {
      setIsTranscribing(false);
      setVolumeLevel(0);

      if (onSpeechEnd) {
        onSpeechEnd();
        setOnSpeechEnd(null);
      }
      return;
    }

    setTranscriptionError(event.error);
    setIsTranscribing(false);
    setVolumeLevel(0);
    reportError(new Error(event.error), {
      context: 'Native speech recognition error',
      tags: { feature: 'voice-input-native' },
    });
  });

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
        interimResults: true,
        maxAlternatives: 1,
        continuous: false,
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
    isNativeAvailable: isAvailable,
    startNativeRecognition,
    stopNativeRecognition,

    // Status
    isTranscribing,
    transcriptionError,
    volumeLevel,
  };
};
