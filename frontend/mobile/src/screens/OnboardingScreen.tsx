import React from 'react';
import {
  View,
  StyleSheet,
  ScrollView,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { theme } from '@/constants/theme';
import { Header, BackButton } from '@/components/ui';
import { DefaultOnboardingSteps as OnboardingStepsComponent } from '@/components/onboarding';  // Simplified

export const OnboardingScreen: React.FC = () => {
  const navigation = useNavigation();

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        <Header 
          title="Setup Guide"
          leftContent={<BackButton onPress={() => navigation.goBack()} />}
        />
        
        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
        >
          <View style={styles.contentContainer}>
            <OnboardingStepsComponent />
          </View>
        </ScrollView>
      </SafeAreaView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.colors.background,
  },
  safeArea: {
    flex: 1,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingTop: theme.spacing.sm,
    paddingBottom: theme.spacing.xl * 2,
  },
  contentContainer: {
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.08)',
    padding: theme.spacing.lg,
    alignItems: 'center',
    marginHorizontal: theme.spacing.lg,
  },
});