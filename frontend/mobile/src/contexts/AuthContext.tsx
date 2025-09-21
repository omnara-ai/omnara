import React, { createContext, useContext, useEffect, useState } from 'react';
import { Platform } from 'react-native';
import { Session, User } from '@supabase/supabase-js';
import { useQueryClient } from '@tanstack/react-query';
import { supabase } from '@/lib/supabase';
import { reportError } from '@/lib/logger';
import { UserProfile } from '@/types';
import { dashboardApi } from '@/services/api';
import * as AppleAuthentication from 'expo-apple-authentication';

// Conditionally import Google Sign-In
let GoogleSignin: any;
let statusCodes: any;
let isGoogleSignInAvailable = false;

try {
  const googleSignInModule = require('@react-native-google-signin/google-signin');
  GoogleSignin = googleSignInModule.GoogleSignin;
  statusCodes = googleSignInModule.statusCodes;
  
  // Configure Google Sign-In
  GoogleSignin.configure({
    webClientId: process.env.EXPO_PUBLIC_GOOGLE_WEB_CLIENT_ID!,
    iosClientId: process.env.EXPO_PUBLIC_GOOGLE_IOS_CLIENT_ID,
    // For testing: androidClientId: process.env.EXPO_PUBLIC_GOOGLE_ANDROID_EXPO_CLIENT_ID
    androidClientId: process.env.EXPO_PUBLIC_GOOGLE_ANDROID_CLIENT_ID,
    offlineAccess: true,
  });
  
  isGoogleSignInAvailable = true;
} catch (error) {
  console.warn('Google Sign-In module not available - this is expected in Expo Go or when the module is not properly linked:', error);
}


interface AuthContextType {
  session: Session | null;
  user: User | null;
  profile: UserProfile | null;
  loading: boolean;
  profileLoading: boolean;
  profileError: string | null;
  signIn: (email: string, password: string) => Promise<void>;
  signUp: (email: string, password: string, fullName?: string) => Promise<void>;
  signInWithGoogleNative: () => Promise<void>;
  signInWithApple: () => Promise<void>;
  signOut: () => Promise<void>;
  refreshProfile: () => Promise<void>;
  retryProfileFetch: () => Promise<void>;
}

const AuthContext = createContext<AuthContextType | undefined>(undefined);

export const useAuth = () => {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
};

export const AuthProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const queryClient = useQueryClient();
  const [session, setSession] = useState<Session | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const [profile, setProfile] = useState<UserProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [profileLoading, setProfileLoading] = useState(false);
  const [profileError, setProfileError] = useState<string | null>(null);

  console.log('[AuthProvider] State - loading:', loading, 'session:', !!session, 'profile:', !!profile, 'profileLoading:', profileLoading);

  useEffect(() => {
    const initializeAuth = async () => {
      try {
        // Just get the Supabase session - this should be instant as it's from local storage
        console.log('[AuthProvider] Initializing Supabase auth...');
        const { data: { session } } = await supabase.auth.getSession();
        
        console.log('[AuthProvider] Supabase session retrieved:', session ? 'Found session' : 'No session');
        setSession(session);
        setUser(session?.user ?? null);
        
        // Set loading to false immediately after getting session
        console.log('[AuthProvider] Setting loading to false');
        setLoading(false);
        
        // Fetch profile in background if we have a session
        if (session?.user) {
          console.log('[AuthProvider] Session exists, fetching profile in background');
          fetchProfileWithRetry().catch(error => {
            console.warn('[AuthProvider] Profile fetch failed:', error);
          });
        }
      } catch (error) {
        reportError(error, {
          context: '[AuthProvider] Error during auth initialization',
          tags: { feature: 'mobile-auth' },
        });
        setLoading(false);
      }
    };

    initializeAuth();

    // Listen for auth changes
    const { data: authListener } = supabase.auth.onAuthStateChange((event, session) => {
      console.log('[AuthProvider] Auth state changed:', event, session ? 'Session exists' : 'No session');
      setSession(session);
      setUser(session?.user ?? null);
      
      if (session?.user) {
        console.log('[AuthProvider] User signed in, clearing old profile and fetching new one');
        // Clear old profile data first
        setProfile(null);
        setProfileError(null);
        // Don't await inside onAuthStateChange - this causes a Supabase deadlock bug
        // See: https://github.com/supabase/gotrue-js/issues/762
        fetchProfileWithRetry().catch(error => {
          console.warn('[AuthProvider] Profile fetch failed:', error);
        });
      } else {
        console.log('[AuthProvider] User signed out, clearing profile and cache');
        // Clear profile when user signs out
        setProfile(null);
        setProfileError(null);
        // Clear React Query cache when user signs out
        queryClient.clear();
      }
    });

    return () => {
      authListener.subscription.unsubscribe();
    };
  }, []);

  const fetchProfileWithRetry = async (showLoading = true) => {
    if (showLoading) {
      console.log('[AuthProvider] Setting profile loading to true');
      setProfileLoading(true);
    }
    setProfileError(null);
    
    try {
      console.log('[AuthProvider] Fetching user profile...');
      const userProfile = await dashboardApi.getCurrentUser();
      console.log('[AuthProvider] User profile fetched successfully:', userProfile.email);
      setProfile(userProfile);
    } catch (error) {
      reportError(error, {
        context: '[AuthProvider] Error fetching user profile',
        tags: { feature: 'mobile-auth' },
      });
      
      const errorMessage = error instanceof Error ? error.message : 'Failed to fetch user profile';
      
      // Check if this is an authentication error (invalid/expired session)
      if (errorMessage.includes('Could not validate credentials') || 
          errorMessage.includes('Session from session_id claim') ||
          errorMessage.includes('JWT') ||
          errorMessage.includes('unauthorized') ||
          errorMessage.includes('No active session')) {
        console.warn('[AuthProvider] Session is invalid or expired, signing user out');
        // Clear the invalid session
        await supabase.auth.signOut();
        setSession(null);
        setUser(null);
        setProfile(null);
        setProfileError(null);
        return;
      }
      
      // For other errors, show them to the user but don't sign them out
      setProfileError(errorMessage);
      console.warn('[AuthProvider] Profile fetch failed, but user session remains valid');
    } finally {
      if (showLoading) {
        console.log('[AuthProvider] Setting profile loading to false');
        setProfileLoading(false);
      }
    }
  };

  const fetchProfile = async () => {
    return fetchProfileWithRetry(false);
  };

  const retryProfileFetch = async () => {
    return fetchProfileWithRetry(true);
  };

  // Note: Backend user sync is not needed - the backend automatically creates users
  // when they exist in Supabase but not in the local database via get_current_user dependency

  const signIn = async (email: string, password: string) => {
    const { data: authData, error } = await supabase.auth.signInWithPassword({
      email,
      password,
    });
    
    if (error) throw error;
    
    if (!authData.user) {
      throw new Error('Invalid credentials');
    }
  };

  const signUp = async (email: string, password: string, fullName?: string) => {
    const { data: authData, error } = await supabase.auth.signUp({
      email,
      password,
      options: {
        data: {
          full_name: fullName,
        },
      },
    });
    
    if (error) throw error;
    
    if (!authData.user) {
      throw new Error('Failed to create user');
    }

    // Check if email confirmation is required
    if (!authData.session && authData.user && !authData.user.email_confirmed_at) {
      // Email confirmation is required - throw a specific error to handle in the UI
      throw new Error('ACCOUNT_CREATED_CONFIRM_EMAIL');
    }
    
    // If we have a session, the user is immediately signed in (email confirmation disabled)
    if (authData.session) {
      console.log('User signed up and signed in immediately');
    }
  };

  const signInWithGoogleNative = async () => {
    try {
      // Check if Google Sign-In module is available
      if (!isGoogleSignInAvailable) {
        throw new Error('Google Sign-In is not available. This feature requires a development build with the Google Sign-In native module properly installed.');
      }

      // Check if we're in Expo Go (development environment)
      const Constants = await import('expo-constants');
      if (Constants.default.executionEnvironment === 'storeClient') {
        throw new Error('Google Sign-In requires a development build. Please use expo run:ios or expo run:android instead of Expo Go.');
      }

      // Check if Google Play Services are available (Android only)
      if (Platform.OS === 'android') {
        await GoogleSignin.hasPlayServices();
      }
      
      const userInfo = await GoogleSignin.signIn();
      
      if (userInfo.idToken) {
        // Use signInWithIdToken without nonce for development builds
        const { data, error } = await supabase.auth.signInWithIdToken({
          provider: 'google',
          token: userInfo.idToken,
        });
        
        if (error) {
          reportError(error, {
            context: 'Supabase auth error',
            tags: { feature: 'mobile-auth' },
          });
          throw error;
        }
        
        console.log('Native Google Sign-In successful:', data);
        
        // The auth state change listener will handle the session update
      } else {
        throw new Error('No ID token present!');
      }
    } catch (error: any) {
      reportError(error, {
        context: 'Native Google Sign-In error',
        tags: { feature: 'mobile-auth' },
      });
      
      if (statusCodes && error.code === statusCodes.SIGN_IN_CANCELLED) {
        throw new Error('Sign-in was cancelled');
      } else if (statusCodes && error.code === statusCodes.IN_PROGRESS) {
        throw new Error('Sign-in is already in progress');
      } else if (statusCodes && error.code === statusCodes.PLAY_SERVICES_NOT_AVAILABLE) {
        throw new Error('Play services not available or outdated');
      }
      
      throw error;
    }
  };

  const signInWithApple = async () => {
    try {
      // Check if Apple Authentication is available (iOS only)
      const isAvailable = await AppleAuthentication.isAvailableAsync();
      if (!isAvailable) {
        throw new Error('Apple Sign-In is not available on this device');
      }

      // Request Apple authentication
      const credential = await AppleAuthentication.signInAsync({
        requestedScopes: [
          AppleAuthentication.AppleAuthenticationScope.FULL_NAME,
          AppleAuthentication.AppleAuthenticationScope.EMAIL,
        ],
      });

      // Sign in via Supabase Auth with the identity token
      if (credential.identityToken) {
        const { error, data } = await supabase.auth.signInWithIdToken({
          provider: 'apple',
          token: credential.identityToken,
        });

        if (error) {
          reportError(error, {
            context: 'Supabase auth error',
            tags: { feature: 'mobile-auth' },
          });
          throw error;
        }

        console.log('Native Apple Sign-In successful:', data);
        
        // The auth state change listener will handle the session update
      } else {
        throw new Error('No identity token received from Apple');
      }
    } catch (error: any) {
      reportError(error, {
        context: 'Apple Sign-In error',
        tags: { feature: 'mobile-auth' },
      });
      
      // Handle specific Apple authentication errors
      if (error.code === 'ERR_REQUEST_CANCELED') {
        throw new Error('Sign in cancelled');
      } else if (error.code === 'ERR_INVALID_OPERATION') {
        throw new Error('Apple Sign-In is not available');
      }
      
      throw error;
    }
  };

  const signOut = async () => {
    // Check if push notifications are active before trying to deactivate
    try {
      const { notificationService } = await import('@/services/notifications');
      
      // First check if push token is active
      const isPushActive = await notificationService.isPushTokenActive();
      
      if (isPushActive) {
        console.log('Push token is active, attempting to deactivate...');
        
        // Add timeout to prevent hanging on network issues
        const timeoutPromise = new Promise((_, reject) => {
          setTimeout(() => reject(new Error('Push token deactivation timeout')), 5000);
        });
        
        await Promise.race([
          notificationService.deactivatePushToken(),
          timeoutPromise
        ]);
        
        console.log('Push token deactivated successfully');
      } else {
        console.log('No active push token found, skipping deactivation');
      }
    } catch (error) {
      // Log the error but don't prevent sign out
      console.warn('Failed to deactivate push token (this is not critical):', error);
      // Continue with sign out even if push token deactivation fails
    }
    
    const { error } = await supabase.auth.signOut({ scope: 'local' });
    if (error) throw error;
    
    // Clear all user data
    setSession(null);
    setUser(null);
    setProfile(null);
    setProfileError(null);
    
    // Clear React Query cache to prevent data from persisting across users
    queryClient.clear();
  };

  const value = {
    session,
    user,
    profile,
    loading,
    profileLoading,
    profileError,
    signIn,
    signUp,
    signInWithGoogleNative,
    signInWithApple,
    signOut,
    refreshProfile: fetchProfile,
    retryProfileFetch,
  };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};
