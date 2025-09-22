import React from 'react';
import {
  Modal,
  View,
  Text,
  StyleSheet,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
  TouchableOpacity,
} from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { X } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import { Button } from './Button';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

interface BaseModalProps {
  visible: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  onSubmit: () => void;
  submitText: string;
  isLoading?: boolean;
  isDisabled?: boolean;
}

export const BaseModal: React.FC<BaseModalProps> = ({
  visible,
  onClose,
  title,
  children,
  onSubmit,
  submitText,
  isLoading = false,
  isDisabled = false,
}) => {
  const insets = useSafeAreaInsets();
  
  return (
    <Modal
      visible={visible}
      animationType="slide"
      transparent={true}
      onRequestClose={onClose}
    >
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={styles.container}
      >
        <View style={styles.backdrop}>
          <View style={styles.modalContent}>
            <LinearGradient
              colors={['rgba(15, 23, 42, 0.98)', 'rgba(15, 23, 42, 0.95)']}
              start={{ x: 0, y: 0 }}
              end={{ x: 0, y: 1 }}
              style={styles.gradientBackground}
            >
              <View style={styles.header}>
                <Text style={styles.title}>{title}</Text>
                <TouchableOpacity onPress={onClose} style={styles.closeButton}>
                  <X size={24} color={theme.colors.textMuted} />
                </TouchableOpacity>
              </View>

              <ScrollView 
                style={styles.content} 
                showsVerticalScrollIndicator={false}
                contentContainerStyle={{ paddingBottom: theme.spacing.xl }}
              >
                {children}
              </ScrollView>

              <View style={styles.footer}>
                <Button
                  onPress={onClose}
                  variant="glass"
                  style={styles.cancelButton}
                  disabled={isLoading}
                >
                  Cancel
                </Button>
                <Button
                  onPress={onSubmit}
                  variant="glass"
                  loading={isLoading}
                  disabled={isDisabled || isLoading}
                  style={styles.submitButton}
                >
                  {submitText}
                </Button>
              </View>
            </LinearGradient>
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  backdrop: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    justifyContent: 'flex-end',
  },
  modalContent: {
    height: 600, // Fixed height
    borderTopLeftRadius: theme.borderRadius.xl,
    borderTopRightRadius: theme.borderRadius.xl,
    overflow: 'hidden',
    ...theme.shadow.xl,
  },
  gradientBackground: {
    flex: 1,
    paddingTop: theme.spacing.lg,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: theme.spacing.lg,
    paddingBottom: theme.spacing.md,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(255, 255, 255, 0.1)',
  },
  title: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  closeButton: {
    padding: theme.spacing.sm,
  },
  content: {
    flex: 1,
    paddingHorizontal: theme.spacing.lg,
    paddingTop: theme.spacing.lg,
  },
  footer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingHorizontal: theme.spacing.lg,
    paddingVertical: theme.spacing.lg,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  cancelButton: {
    flex: 1,
    marginRight: theme.spacing.sm,
  },
  submitButton: {
    flex: 1,
    marginLeft: theme.spacing.sm,
  },
});