import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  StyleSheet,
  Platform,
  Alert,
  Image,
  Pressable,
  Keyboard,
  Animated,
} from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { Button, Input, Gradient, GoogleSignInButton } from '@/components/ui';
import { AppleSignInButton } from '@/components/AppleSignInButton';
import { useAuth } from '@/contexts/AuthContext';
import { theme } from '@/constants/theme';

export const LoginScreen: React.FC = () => {
  const navigation = useNavigation();
  const { signIn, signInWithApple } = useAuth();
  const insets = useSafeAreaInsets();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [appleLoading, setAppleLoading] = useState(false);
  const [showLoginFields, setShowLoginFields] = useState(false);
  const animatedBottom = React.useRef(new Animated.Value(-50)).current;
  
  // Typing animation state
  const locations = useRef(['the park', 'your bed', 'the beach', 'the toilet', 'the gym', 'a date', 'anywhere.']).current;
  const [typingText, setTypingText] = useState(locations[0]);
  const currentIndexRef = useRef(0);

  // Typing animation effect
  useEffect(() => {
    let isMounted = true;
    
    const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
    
    const runAnimation = async () => {
      while (isMounted) {
        // Get current and next text
        const currentWord = locations[currentIndexRef.current];
        const nextIndex = (currentIndexRef.current + 1) % locations.length;
        const nextWord = locations[nextIndex];
        const isAnywhere = nextWord === 'anywhere.';
        
        // Wait before starting to backspace
        const waitTime = currentWord === 'anywhere.' ? 7000 : 2500;
        await sleep(waitTime);
        if (!isMounted) break;
        
        // Backspace animation
        for (let i = currentWord.length; i >= 0; i--) {
          if (!isMounted) break;
          setTypingText(currentWord.slice(0, i));
          await sleep(60);
        }
        
        // Pause between backspace and typing
        await sleep(300);
        if (!isMounted) break;
        
        // Type new word (slower for "anywhere.")
        const typingSpeed = isAnywhere ? 175 : 100;
        for (let i = 0; i <= nextWord.length; i++) {
          if (!isMounted) break;
          setTypingText(nextWord.slice(0, i));
          await sleep(typingSpeed);
        }
        
        // Update index for next iteration
        currentIndexRef.current = nextIndex;
      }
    };
    
    // Start the animation loop
    runAnimation();
    
    return () => {
      isMounted = false;
    };
  }, [locations]);

  React.useEffect(() => {
    const keyboardWillShow = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow',
      (e) => {
        if (showLoginFields) {
          Animated.timing(animatedBottom, {
            toValue: e.endCoordinates.height - 50,
            duration: e.duration || 250,
            useNativeDriver: false,
          }).start();
        }
      }
    );
    
    const keyboardWillHide = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide',
      (e) => {
        Animated.timing(animatedBottom, {
          toValue: -50,
          duration: e.duration || 250,
          useNativeDriver: false,
        }).start();
      }
    );

    return () => {
      keyboardWillShow.remove();
      keyboardWillHide.remove();
    };
  }, [showLoginFields, animatedBottom]);

  const handleLogin = async () => {
    if (!showLoginFields) {
      setShowLoginFields(true);
      return;
    }

    if (!email || !password) {
      Alert.alert('Error', 'Please fill in all fields');
      return;
    }

    setLoading(true);
    try {
      await signIn(email, password);
    } catch (error: any) {
      Alert.alert('Login Failed', error.message);
    } finally {
      setLoading(false);
    }
  };

  const handleAppleSignIn = async () => {
    setAppleLoading(true);
    try {
      await signInWithApple();
    } catch (error: any) {
      if (error.message !== 'Sign in cancelled') {
        Alert.alert('Apple Sign-In Failed', error.message);
      }
    } finally {
      setAppleLoading(false);
    }
  };


  return (
    <Gradient variant="dark" style={styles.container}>
      <View style={styles.backgroundContainer}>
        <SafeAreaView style={styles.safeArea}>
          <View style={styles.content}>
            <View style={styles.logoContainer}>
              <Image
                source={require('../../../assets/icon.png')}
                style={styles.logo}
                resizeMode="contain"
              />
              <View style={styles.taglineContainer}>
                <Text style={styles.tagline}>Command your agents</Text>
                <Text style={styles.tagline}>from <Text style={styles.taglineItalic}>{typingText}</Text></Text>
              </View>
            </View>
          </View>

          <Animated.View style={[
            styles.signInContainer,
            { 
              bottom: animatedBottom,
              paddingBottom: insets.bottom + 50 // Dynamic padding based on safe area
            }
          ]}>
            <View style={styles.bottomContent}>
                  {showLoginFields && (
                    <View style={styles.fieldsContainer}>
                      <View style={styles.formHeader}>
                        <Text style={styles.formTitle}>Welcome back</Text>
                        <Text style={styles.formSubtitle}>Sign in to your account</Text>
                      </View>
                      
                      <View style={styles.inputWrapper}>
                        <Input
                          placeholder="Email"
                          value={email}
                          onChangeText={setEmail}
                          keyboardType="email-address"
                          autoCapitalize="none"
                          autoComplete="email"
                        />
                      </View>

                      <View style={styles.inputWrapper}>
                        <Input
                          placeholder="Password"
                          value={password}
                          onChangeText={setPassword}
                          secureTextEntry
                          autoComplete="password"
                        />
                      </View>
                    </View>
                  )}

                <View style={styles.buttonsContainer}>
                  {!showLoginFields ? (
                    <>
                      <GoogleSignInButton disabled={loading || appleLoading} />
                      <AppleSignInButton onPress={handleAppleSignIn} loading={appleLoading} />
                      <Button
                        onPress={handleLogin}
                        loading={loading}
                        fullWidth
                        size="lg"
                        variant="glass"
                      >
                        Email Login
                      </Button>
                      <Button
                        onPress={() => navigation.navigate('SignUp' as never)}
                        fullWidth
                        size="lg"
                        variant="glass"
                      >
                        Sign Up
                      </Button>
                    </>
                  ) : (
                    <>
                      <Button
                        onPress={handleLogin}
                        loading={loading}
                        fullWidth
                        size="lg"
                        variant="glass"
                      >
                        Sign In
                      </Button>
                      <Pressable onPress={() => setShowLoginFields(false)} style={styles.cancelButton}>
                        <Text style={styles.cancelText}>Cancel</Text>
                      </Pressable>
                    </>
                  )}
                  </View>
              </View>
            </Animated.View>
        </SafeAreaView>
      </View>
    </Gradient>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  backgroundContainer: {
    flex: 1,
    backgroundColor: theme.colors.background,
  },
  safeArea: {
    flex: 1,
  },
  content: {
    flex: 1,
  },
  logoContainer: {
    position: 'absolute',
    top: '25%',
    left: 0,
    right: 0,
    alignItems: 'center',
    zIndex: 0,
  },
  logo: {
    width: 140,
    height: 140,
    marginBottom: theme.spacing.sm,
  },
  taglineContainer: {
    alignItems: 'center',
  },
  tagline: {
    fontSize: theme.fontSize['2xl'],
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    textAlign: 'center',
    lineHeight: 32,
  },
  taglineItalic: {
    fontFamily: theme.fontFamily.regularItalic,
  },
  signInContainer: {
    position: 'absolute',
    bottom: -50, // Extend beyond screen bottom
    left: 0,
    right: 0,
    backgroundColor: theme.colors.authContainer,
    borderTopLeftRadius: theme.borderRadius.xl,
    borderTopRightRadius: theme.borderRadius.xl,
    paddingHorizontal: theme.spacing.xl,
    paddingBottom: 50, // Base padding, will be adjusted dynamically
    minHeight: 'auto',
    zIndex: 1,
  },
  bottomContent: {
    paddingTop: theme.spacing.lg,
  },
  fieldsContainer: {
    marginBottom: theme.spacing.lg,
  },
  formHeader: {
    marginBottom: theme.spacing.lg,
  },
  formTitle: {
    fontSize: theme.fontSize['2xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    marginBottom: 2,
  },
  formSubtitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  inputWrapper: {
    marginBottom: theme.spacing.sm,
  },
  cancelButton: {
    alignSelf: 'center',
    paddingVertical: theme.spacing.xs,
    paddingHorizontal: theme.spacing.md,
    marginTop: theme.spacing.xs,
  },
  cancelText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  buttonsContainer: {
    gap: theme.spacing.sm,
  },
});