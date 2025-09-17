import React, { useEffect } from 'react';
import { Platform } from 'react-native';
import { NavigationContainer } from '@react-navigation/native';
import { createNativeStackNavigator } from '@react-navigation/native-stack';
import { createDrawerNavigator } from '@react-navigation/drawer';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import { SafeAreaProvider, initialWindowMetrics } from 'react-native-safe-area-context';
import { StatusBar } from 'expo-status-bar';
import * as ExpoSplashScreen from 'expo-splash-screen';
import { AuthProvider, useAuth } from '@/contexts/AuthContext';
import { theme } from '@/constants/theme';
import { useFonts } from '@/hooks/useFonts';
import { useNotifications } from '@/hooks/useNotifications';
import { ProfileSidebar } from '@/components/navigation/ProfileSidebar';
import { subscriptionService } from '@/services/subscriptionService';
import { REVENUECAT_API_KEY } from '@/config/env';

// Import screens
import { SplashScreen } from '@/screens/SplashScreen';
import { LoginScreen } from '@/screens/auth/LoginScreen';
import { SignUpScreen } from '@/screens/auth/SignUpScreen';
import { MainScreen } from '@/screens/MainScreen';
import { AllInstancesScreen } from '@/screens/instances/AllInstancesScreen';
import { InstanceDetailScreen } from '@/screens/dashboard/InstanceDetailScreen';
import { APIKeysScreen } from '@/screens/dashboard/APIKeysScreen';
import { NotificationSettingsScreen } from '@/screens/dashboard/NotificationSettingsScreen';
import { SubscriptionScreen } from '@/screens/dashboard/SubscriptionScreen';
import { LegalScreen } from '@/screens/LegalScreen';
import { OnboardingScreen } from '@/screens/OnboardingScreen';

// Keep the splash screen visible while we fetch resources
ExpoSplashScreen.preventAutoHideAsync();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 2,
      staleTime: 1000 * 60 * 5, // 5 minutes
    },
  },
});

const Stack = createNativeStackNavigator();
const Drawer = createDrawerNavigator();
const AuthStack = createNativeStackNavigator();

function AuthNavigator() {
  console.log('[AuthNavigator] Component rendered');
  
  return (
    <AuthStack.Navigator
      screenOptions={{
        headerShown: false,
        contentStyle: { backgroundColor: theme.colors.background },
      }}
    >
      <AuthStack.Screen name="Login" component={LoginScreen} />
      <AuthStack.Screen name="SignUp" component={SignUpScreen} />
    </AuthStack.Navigator>
  );
}

function DrawerNavigator() {
  console.log('[DrawerNavigator] Component rendered');
  
  return (
    <Drawer.Navigator
      initialRouteName="Dashboard"
      drawerContent={(props) => <ProfileSidebar {...props} />}
      screenOptions={{
        headerShown: false,
        drawerStyle: {
          backgroundColor: theme.colors.background,
          width: 280,
        },
        drawerType: 'slide',
        overlayColor: 'rgba(0, 0, 0, 0.5)',
      }}
    >
      <Drawer.Screen
        name="Dashboard"
        component={MainScreen}
      />
    </Drawer.Navigator>
  );
}

function RootNavigator() {
  const { session, loading } = useAuth();
  
  console.log('[RootNavigator] Component rendered - session:', !!session, 'loading:', loading);
  
  // Initialize notification system when user is authenticated
  useNotifications();
  
  // Initialize RevenueCat when user is authenticated
  useEffect(() => {
    const initializeRevenueCat = async () => {
      if (Platform.OS === 'ios' && REVENUECAT_API_KEY && session?.user?.id) {
        try {
          await subscriptionService.initialize(REVENUECAT_API_KEY, session.user.id);
          console.log('[RootNavigator] RevenueCat initialized successfully for user:', session.user.id);
        } catch (error) {
          console.error('[RootNavigator] Failed to initialize RevenueCat:', error);
        }
      }
    };

    initializeRevenueCat();
  }, [session?.user?.id]);

  if (loading) {
    console.log('[RootNavigator] Showing splash screen - loading');
    return <SplashScreen />;
  }

  console.log('[RootNavigator] Navigation ready - session exists:', !!session);

  return (
    <Stack.Navigator
      initialRouteName={session ? "MainDrawer" : "Auth"}
      screenOptions={{
        headerShown: false,
        contentStyle: { backgroundColor: theme.colors.background },
      }}
    >
      {session ? (
        <>
          <Stack.Screen name="MainDrawer" component={DrawerNavigator} />
          <Stack.Screen
            name="InstanceDetail"
            component={InstanceDetailScreen}
            options={{
              headerShown: false,
            }}
          />
          <Stack.Screen
            name="APIKeys"
            component={APIKeysScreen}
            options={{
              headerShown: false,
            }}
          />
          <Stack.Screen
            name="NotificationSettings"
            component={NotificationSettingsScreen}
            options={{
              headerShown: false,
            }}
          />
          <Stack.Screen
            name="Subscription"
            component={SubscriptionScreen}
            options={{
              headerShown: false,
            }}
          />
          <Stack.Screen
            name="AllInstances"
            component={AllInstancesScreen}
            options={{
              headerShown: false,
            }}
          />
          <Stack.Screen
            name="Legal"
            component={LegalScreen}
            options={{
              headerShown: false,
            }}
          />
          <Stack.Screen
            name="Onboarding"
            component={OnboardingScreen}
            options={{
              headerShown: false,
            }}
          />
        </>
      ) : (
        <Stack.Screen name="Auth" component={AuthNavigator} />
      )}
    </Stack.Navigator>
  );
}

export default function App() {
  const { fontsLoaded, fontError } = useFonts();

  console.log('[App] Component rendered - fontsLoaded:', fontsLoaded, 'fontError:', fontError);

  useEffect(() => {
    console.log('[App] useEffect triggered - fontsLoaded:', fontsLoaded, 'fontError:', fontError);
    if (fontsLoaded || fontError) {
      console.log('[App] Hiding splash screen');
      ExpoSplashScreen.hideAsync();
    }
  }, [fontsLoaded, fontError]);

  // RevenueCat initialization moved to RootNavigator where we have user context

  if (!fontsLoaded && !fontError) {
    console.log('[App] Waiting for fonts to load');
    return null;
  }

  console.log('[App] Rendering main app');

  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <SafeAreaProvider initialMetrics={initialWindowMetrics}>
        <QueryClientProvider client={queryClient}>
          <AuthProvider>
            <NavigationContainer
              linking={{
                prefixes: ['omnara://', 'com.omnara.app://'],
                config: {
                  screens: {
                    Auth: {
                      screens: {
                        Login: 'auth/callback',
                        SignUp: 'auth/callback',
                      },
                    },
                    MainDrawer: {
                      screens: {
                        Dashboard: 'dashboard',
                      },
                    },
                    InstanceDetail: 'instances/:instanceId',
                  },
                },
              }}
              onStateChange={(state) => {
                console.log('[NavigationContainer] Navigation state changed:', JSON.stringify(state?.routes?.[0]?.name, null, 2));
              }}
              onReady={() => {
                console.log('[NavigationContainer] Navigation ready');
              }}
            >
              <StatusBar style="light" />
              <RootNavigator />
            </NavigationContainer>
          </AuthProvider>
        </QueryClientProvider>
      </SafeAreaProvider>
    </GestureHandlerRootView>
  );
}
