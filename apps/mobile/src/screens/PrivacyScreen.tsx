import React from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
} from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { ChevronLeft } from 'lucide-react-native';
import { theme } from '@/constants/theme';

export const PrivacyScreen: React.FC = () => {
  const navigation = useNavigation();
  const insets = useSafeAreaInsets();

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        {/* Header */}
        <View style={[styles.header, { paddingTop: insets.top + theme.spacing.xs }]}>
          <TouchableOpacity 
            style={styles.backButton}
            onPress={() => navigation.goBack()}
            activeOpacity={0.7}
          >
            <ChevronLeft size={18} color={theme.colors.white} strokeWidth={2} />
          </TouchableOpacity>
          <Text style={styles.title}>Privacy Policy</Text>
          <View style={styles.headerSpacer} />
        </View>

        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
        >
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Your Privacy Matters</Text>
            <Text style={styles.paragraph}>
              At Omnara, we take your privacy seriously. This Privacy Policy explains how we collect, 
              use, and protect your information when you use our AI agent communication platform.
            </Text>
          </View>

          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Information We Collect</Text>
            <Text style={styles.paragraph}>
              We collect information you provide directly to us, such as when you create an account, 
              configure AI agents, or interact with our services. This may include your email address, 
              display name, and agent configuration data.
            </Text>
          </View>

          <View style={styles.section}>
            <Text style={styles.sectionTitle}>How We Use Your Information</Text>
            <Text style={styles.paragraph}>
              We use the information we collect to provide, maintain, and improve our services, 
              including to facilitate communication between you and your AI agents, send you 
              notifications, and respond to your requests.
            </Text>
          </View>

          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Data Security</Text>
            <Text style={styles.paragraph}>
              We implement appropriate technical and organizational measures to protect your personal 
              information against unauthorized access, alteration, disclosure, or destruction.
            </Text>
          </View>

          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Your Rights</Text>
            <Text style={styles.paragraph}>
              You have the right to access, update, or delete your personal information. You can 
              manage your data through your account settings or by contacting our support team.
            </Text>
          </View>

          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Contact Us</Text>
            <Text style={styles.paragraph}>
              If you have any questions about this Privacy Policy or our practices, please contact 
              us at contact@omnara.com
            </Text>
          </View>

          <View style={styles.footer}>
            <Text style={styles.footerText}>
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
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: theme.spacing.sm,
    paddingBottom: 2,
    backgroundColor: theme.colors.background,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(255, 255, 255, 0.1)',
  },
  backButton: {
    width: 28,
    height: 28,
    justifyContent: 'center',
    alignItems: 'center',
  },
  title: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
    letterSpacing: -0.3,
  },
  headerSpacer: {
    width: 28, // Same as backButton width to center the title
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    padding: theme.spacing.lg,
    paddingTop: theme.spacing.md,
    paddingBottom: theme.spacing.xl * 2,
  },
  section: {
    marginBottom: theme.spacing.xl,
  },
  sectionTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  paragraph: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.8)',
    lineHeight: 24,
  },
  footer: {
    marginTop: theme.spacing.xl,
    paddingTop: theme.spacing.xl,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  footerText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
    textAlign: 'center',
  },
});