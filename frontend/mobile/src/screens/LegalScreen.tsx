import React from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  Linking,
  ScrollView,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { ExternalLink, Shield, FileText } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import { Header } from '@/components/ui';
import { reportError } from '@/lib/logger';

const PRIVACY_POLICY_URL = 'https://omnara.com/privacy';
const TERMS_OF_USE_URL = 'https://omnara.com/terms';

export const LegalScreen: React.FC = () => {
  const navigation = useNavigation();

  const openLink = (url: string) => {
    Linking.openURL(url).catch(error => {
      reportError(error, {
        context: 'Failed to open URL',
        extras: { url },
        tags: { feature: 'mobile-legal' },
      });
    });
  };

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        <Header 
          title="Legal"
          onBack={() => navigation.goBack()}
        />
        
        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
        >
          <View style={styles.introSection}>
            <Text style={styles.introText}>
              View our legal documents and policies
            </Text>
          </View>

          {/* Privacy Policy Button */}
          <TouchableOpacity
            style={styles.linkButton}
            onPress={() => openLink(PRIVACY_POLICY_URL)}
            activeOpacity={0.7}
          >
            <View style={styles.iconContainer}>
              <Shield size={24} color={theme.colors.pro} strokeWidth={2} />
            </View>
            <View style={styles.linkContent}>
              <Text style={styles.linkTitle}>Privacy Policy</Text>
              <Text style={styles.linkDescription}>
                Learn how we collect, use, and protect your information
              </Text>
            </View>
            <ExternalLink size={20} color={theme.colors.primaryLight} strokeWidth={2} />
          </TouchableOpacity>

          {/* Terms of Use Button */}
          <TouchableOpacity
            style={styles.linkButton}
            onPress={() => openLink(TERMS_OF_USE_URL)}
            activeOpacity={0.7}
          >
            <View style={styles.iconContainer}>
              <FileText size={24} color={theme.colors.pro} strokeWidth={2} />
            </View>
            <View style={styles.linkContent}>
              <Text style={styles.linkTitle}>Terms of Use</Text>
              <Text style={styles.linkDescription}>
                Review our terms and conditions for using Omnara
              </Text>
            </View>
            <ExternalLink size={20} color={theme.colors.primaryLight} strokeWidth={2} />
          </TouchableOpacity>

          <View style={styles.footer}>
            <Text style={styles.footerText}>
              By using Omnara, you agree to our Terms of Use and Privacy Policy
            </Text>
            <Text style={styles.lastUpdated}>
              Last updated: {new Date().toLocaleDateString()}
            </Text>
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
    padding: theme.spacing.lg,
    paddingTop: theme.spacing.xl,
    paddingBottom: theme.spacing.xl * 2,
  },
  introSection: {
    marginBottom: theme.spacing.xl,
  },
  introText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    textAlign: 'center',
  },
  linkButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.lg,
    marginBottom: theme.spacing.md,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
  },
  iconContainer: {
    width: 48,
    height: 48,
    borderRadius: theme.borderRadius.md,
    backgroundColor: 'rgba(212, 173, 252, 0.1)',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: theme.spacing.md,
  },
  linkContent: {
    flex: 1,
  },
  linkTitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  linkDescription: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  footer: {
    marginTop: theme.spacing.xl * 2,
    paddingTop: theme.spacing.xl,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
    alignItems: 'center',
  },
  footerText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
    textAlign: 'center',
    marginBottom: theme.spacing.sm,
  },
  lastUpdated: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.3)',
  },
});
