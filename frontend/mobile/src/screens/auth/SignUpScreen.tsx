import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
  Alert,
  Image,
  Pressable,
} from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { Button, Input, Gradient, GoogleSignInButton } from '@/components/ui';
import { AppleSignInButton } from '@/components/AppleSignInButton';
import { useAuth } from '@/contexts/AuthContext';
import { theme } from '@/constants/theme';
import { Ionicons } from '@expo/vector-icons';

export const SignUpScreen: React.FC = () => {
  const navigation = useNavigation();
  const { signUp, signInWithApple } = useAuth();
  const insets = useSafeAreaInsets();
  const [fullName, setFullName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [appleLoading, setAppleLoading] = useState(false);
  const [showConfirmationMessage, setShowConfirmationMessage] = useState(false);
  const [userEmail, setUserEmail] = useState('');

  const handleSignUp = async () => {
    if (!fullName || !email || !password || !confirmPassword) {
      Alert.alert('Error', 'Please fill in all fields');
      return;
    }

    if (password !== confirmPassword) {
      Alert.alert('Error', 'Passwords do not match');
      return;
    }

    if (password.length < 6) {
      Alert.alert('Error', 'Password must be at least 6 characters long');
      return;
    }

    setLoading(true);
    try {
      await signUp(email, password, fullName);
      // If we reach here, signup was successful and user is signed in immediately
    } catch (error: any) {
      if (error.message === 'ACCOUNT_CREATED_CONFIRM_EMAIL') {
        setUserEmail(email);
        setShowConfirmationMessage(true);
      } else {
        Alert.alert('Sign Up Failed', error.message);
      }
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
          <KeyboardAvoidingView
            behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
            style={styles.keyboardView}
          >
            <ScrollView
              contentContainerStyle={[
                styles.scrollContent,
                { paddingBottom: insets.bottom + theme.spacing.xl }
              ]}
              showsVerticalScrollIndicator={false}
            >
              <View style={styles.header}>
                <Image
                  source={require('../../../assets/icon.png')}
                  style={styles.logo}
                  resizeMode="contain"
                />
                <Text style={styles.title}>
                  {showConfirmationMessage ? 'Check Your Email' : 'Create Your Account'}
                </Text>
              </View>

              {showConfirmationMessage ? (
                <View style={styles.confirmationContainer}>
                  <View style={styles.confirmationContent}>
                    <Ionicons name="mail-outline" size={60} color={theme.colors.white} style={styles.mailIcon} />
                    <Text style={styles.confirmationTitle}>Confirmation Email Sent!</Text>
                    <Text style={styles.confirmationText}>
                      We've sent a confirmation email to:
                    </Text>
                    <Text style={styles.emailText}>{userEmail}</Text>
                    <Text style={styles.confirmationInstructions}>
                      Please check your inbox and click the confirmation link to activate your account.
                    </Text>
                    <Button
                      onPress={() => navigation.navigate('Login' as never)}
                      fullWidth
                      size="lg"
                      variant="glass"
                      style={{ marginTop: theme.spacing.lg }}
                    >
                      Go to Login
                    </Button>
                  </View>
                </View>
              ) : (
              <View style={styles.formContainer}>
                <View style={styles.form}>
                  <GoogleSignInButton disabled={loading || appleLoading} />
                  <AppleSignInButton onPress={handleAppleSignIn} loading={appleLoading} />
                  
                  <View style={styles.divider}>
                    <View style={styles.dividerLine} />
                    <Text style={styles.dividerText}>or</Text>
                    <View style={styles.dividerLine} />
                  </View>

                  <Input
                    placeholder="Full Name"
                    value={fullName}
                    onChangeText={setFullName}
                    autoCapitalize="words"
                    autoComplete="name"
                  />

                  <Input
                    placeholder="Email"
                    value={email}
                    onChangeText={setEmail}
                    keyboardType="email-address"
                    autoCapitalize="none"
                    autoComplete="email"
                  />

                  <Input
                    placeholder="Password"
                    value={password}
                    onChangeText={setPassword}
                    secureTextEntry
                    autoComplete="new-password"
                  />

                  <Input
                    placeholder="Confirm Password"
                    value={confirmPassword}
                    onChangeText={setConfirmPassword}
                    secureTextEntry
                    autoComplete="new-password"
                  />

                  <Button
                    onPress={handleSignUp}
                    loading={loading}
                    fullWidth
                    size="lg"
                    variant="glass"
                  >
                    Create Account
                  </Button>
                </View>
              </View>
              )}

              {!showConfirmationMessage && (
              <View style={styles.footer}>
                <Pressable onPress={() => navigation.goBack()}>
                  <Text style={styles.footerLink}>Cancel</Text>
                </Pressable>
              </View>
              )}
            </ScrollView>
          </KeyboardAvoidingView>
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
  keyboardView: {
    flex: 1,
  },
  scrollContent: {
    flexGrow: 1,
    paddingHorizontal: theme.spacing.xl,
    paddingTop: theme.spacing.xl,
    paddingBottom: theme.spacing.xl,
  },
  header: {
    alignItems: 'center',
    marginBottom: theme.spacing.xl,
  },
  logo: {
    width: 100,
    height: 100,
    marginBottom: theme.spacing.md,
  },
  title: {
    fontSize: theme.fontSize['2xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    textAlign: 'center',
  },
  formContainer: {
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.xl,
    padding: theme.spacing.xl,
    marginBottom: theme.spacing.xl,
  },
  form: {
    gap: theme.spacing.sm,
  },
  footer: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.spacing.xs,
    marginBottom: theme.spacing.xl,
  },
  footerText: {
    color: 'rgba(255, 255, 255, 0.7)',
    fontFamily: theme.fontFamily.regular,
    fontSize: theme.fontSize.base,
  },
  footerLink: {
    color: theme.colors.white,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    fontSize: theme.fontSize.base,
  },
  divider: {
    flexDirection: 'row',
    alignItems: 'center',
    marginVertical: theme.spacing.md,
  },
  dividerLine: {
    flex: 1,
    height: 1,
    backgroundColor: 'rgba(255, 255, 255, 0.2)',
  },
  dividerText: {
    marginHorizontal: theme.spacing.md,
    color: 'rgba(255, 255, 255, 0.5)',
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
  },
  confirmationContainer: {
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.xl,
    padding: theme.spacing.xl,
    marginBottom: theme.spacing.xl,
  },
  confirmationContent: {
    alignItems: 'center',
  },
  mailIcon: {
    marginBottom: theme.spacing.lg,
  },
  confirmationTitle: {
    fontSize: theme.fontSize['xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.md,
    textAlign: 'center',
  },
  confirmationText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.8)',
    marginBottom: theme.spacing.xs,
    textAlign: 'center',
  },
  emailText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.lg,
    textAlign: 'center',
  },
  confirmationInstructions: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    textAlign: 'center',
    paddingHorizontal: theme.spacing.md,
    lineHeight: theme.fontSize.sm * 1.5,
  },
});