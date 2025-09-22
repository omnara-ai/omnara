import React, { useEffect, useState, useCallback } from 'react';
import {
  ActivityIndicator,
  FlatList,
  KeyboardAvoidingView,
  Modal,
  Platform,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  TouchableWithoutFeedback,
  View,
} from 'react-native';
import { InstanceAccessLevel, InstanceShare } from '@/types';
import { theme } from '@/constants/theme';
import { ChevronDown, X } from 'lucide-react-native';

interface ShareAccessModalProps {
  visible: boolean;
  shares: InstanceShare[];
  loading: boolean;
  shareEmail: string;
  shareAccess: InstanceAccessLevel;
  shareSubmitting: boolean;
  shareError: string | null;
  onClose: () => void;
  onEmailChange: (email: string) => void;
  onAccessChange: (access: InstanceAccessLevel) => void;
  onAddShare: () => void;
  onRemoveShare: (shareId: string) => void;
}

export const ShareAccessModal: React.FC<ShareAccessModalProps> = ({
  visible,
  shares,
  loading,
  shareEmail,
  shareAccess,
  shareSubmitting,
  shareError,
  onClose,
  onEmailChange,
  onAccessChange,
  onAddShare,
  onRemoveShare,
}) => {
  const [isAccessMenuOpen, setAccessMenuOpen] = useState(false);

  useEffect(() => {
    if (!visible) {
      setAccessMenuOpen(false);
    }
  }, [visible]);

  const handleSelectAccess = useCallback(
    (level: InstanceAccessLevel) => {
      onAccessChange(level);
      setAccessMenuOpen(false);
    },
    [onAccessChange]
  );

  const renderShareItem = useCallback(
    ({ item }: { item: InstanceShare }) => (
      <View style={styles.shareItem}>
        <View style={styles.shareItemText}>
          <Text style={styles.shareItemEmail} numberOfLines={1} ellipsizeMode="tail">
            {item.email}
            {item.is_owner ? ' (Owner)' : ''}
          </Text>
          <Text style={styles.shareItemMeta} numberOfLines={1} ellipsizeMode="tail">
            {item.display_name ? `${item.display_name} • ` : ''}
            {item.access === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
            {item.invited ? ' • Pending' : ''}
          </Text>
        </View>
        {!item.is_owner && (
          <TouchableOpacity
            onPress={() => onRemoveShare(item.id)}
            style={styles.shareRemoveButton}
          >
            <Text style={styles.shareRemoveText}>Remove</Text>
          </TouchableOpacity>
        )}
      </View>
    ),
    [onRemoveShare]
  );

  return (
    <Modal
      visible={visible}
      transparent
      animationType="fade"
      onRequestClose={onClose}
    >
      <View style={styles.backdrop}>
        <TouchableWithoutFeedback onPress={onClose}>
          <View style={styles.backdropTouchable} />
        </TouchableWithoutFeedback>

        <KeyboardAvoidingView
          behavior={Platform.OS === 'ios' ? 'padding' : undefined}
          style={styles.modalWrapper}
        >
          <TouchableWithoutFeedback onPress={() => setAccessMenuOpen(false)}>
            <View style={styles.modalContainer}>
              <View style={styles.modalHeader}>
                <Text style={styles.modalTitle}>Shared Access</Text>
                <TouchableOpacity
                  onPress={onClose}
                  style={styles.modalCloseButton}
                  accessibilityRole="button"
                  accessibilityLabel="Close sharing settings"
                >
                  <X size={16} color={theme.colors.textMuted} />
                </TouchableOpacity>
              </View>
              <Text style={styles.modalSubtitle}>
                Manage who can view or edit this session.
              </Text>

              <View style={styles.shareListContainer}>
                {loading ? (
                  <ActivityIndicator size="small" color={theme.colors.primary} />
                ) : shares.length === 0 ? (
                  <Text style={styles.shareEmptyText}>No one else has access yet.</Text>
                ) : (
                  <FlatList
                    data={shares}
                    keyExtractor={(item) => item.id}
                    renderItem={renderShareItem}
                    contentContainerStyle={styles.shareListContent}
                    ItemSeparatorComponent={() => <View style={styles.shareItemSeparator} />}
                    keyboardShouldPersistTaps="handled"
                  />
                )}
              </View>

              <View style={styles.shareForm}>
                <Text style={styles.formLabel}>Invite by email</Text>
                <TextInput
                  value={shareEmail}
                  onChangeText={onEmailChange}
                  placeholder="user@example.com"
                  placeholderTextColor={theme.colors.textMuted}
                  keyboardType="email-address"
                  autoCapitalize="none"
                  autoCorrect={false}
                  style={styles.input}
                />

                <Text style={styles.formLabel}>Access level</Text>
                <View style={styles.accessSelectWrapper}>
                  <TouchableOpacity
                    onPress={() => setAccessMenuOpen((prev) => !prev)}
                    style={styles.accessSelect}
                    activeOpacity={0.8}
                  >
                    <Text style={styles.accessSelectText}>
                      {shareAccess === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
                    </Text>
                    <ChevronDown size={16} color={theme.colors.textMuted} />
                  </TouchableOpacity>
                  {isAccessMenuOpen && (
                    <View style={styles.accessDropdown}>
                      {[InstanceAccessLevel.WRITE, InstanceAccessLevel.READ].map((level) => (
                        <TouchableOpacity
                          key={level}
                          onPress={() => handleSelectAccess(level)}
                          style={[
                            styles.accessDropdownItem,
                            shareAccess === level && styles.accessDropdownItemActive,
                          ]}
                        >
                          <Text
                            style={[
                              styles.accessDropdownText,
                              shareAccess === level && styles.accessDropdownTextActive,
                            ]}
                          >
                            {level === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
                          </Text>
                        </TouchableOpacity>
                      ))}
                    </View>
                  )}
                </View>

                {shareError && (
                  <Text style={styles.errorText}>{shareError}</Text>
                )}

                <TouchableOpacity
                  onPress={onAddShare}
                  style={styles.submitButton}
                  disabled={shareSubmitting}
                >
                  <Text style={styles.submitText}>
                    {shareSubmitting ? 'Adding…' : 'Add'}
                  </Text>
                </TouchableOpacity>
              </View>
            </View>
          </TouchableWithoutFeedback>
        </KeyboardAvoidingView>
      </View>
    </Modal>
  );
};

const styles = StyleSheet.create({
  backdrop: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.6)',
    justifyContent: 'center',
    paddingHorizontal: theme.spacing.lg,
  },
  backdropTouchable: {
    ...StyleSheet.absoluteFillObject,
  },
  modalWrapper: {
    flex: 1,
    justifyContent: 'center',
  },
  modalContainer: {
    backgroundColor: theme.colors.cardSurface,
    borderRadius: theme.borderRadius.xl,
    padding: theme.spacing.lg,
    borderWidth: 1,
    borderColor: theme.colors.border,
    gap: theme.spacing.md,
  },
  modalHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  modalTitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  modalCloseButton: {
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: theme.colors.panelSurface,
    justifyContent: 'center',
    alignItems: 'center',
  },
  modalSubtitle: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareListContainer: {
    maxHeight: 220,
  },
  shareEmptyText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareListContent: {
    gap: theme.spacing.sm,
  },
  shareItemSeparator: {
    height: theme.spacing.xs,
  },
  shareItem: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    borderRadius: theme.borderRadius.lg,
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
    backgroundColor: theme.colors.panelSurface,
  },
  shareItemText: {
    flex: 1,
    marginRight: theme.spacing.sm,
  },
  shareItemEmail: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
  },
  shareItemMeta: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareRemoveButton: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
  },
  shareRemoveText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.error,
  },
  shareForm: {
    gap: theme.spacing.sm,
  },
  formLabel: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.textMuted,
    textTransform: 'uppercase',
    letterSpacing: 0.6,
  },
  input: {
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    borderRadius: theme.borderRadius.md,
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    color: theme.colors.white,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    backgroundColor: theme.colors.background,
  },
  accessSelectWrapper: {
    position: 'relative',
    zIndex: 10,
    marginBottom: theme.spacing.sm,
  },
  accessSelect: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    borderRadius: theme.borderRadius.md,
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    backgroundColor: theme.colors.background,
  },
  accessSelectText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
  },
  accessDropdown: {
    position: 'absolute',
    top: '110%',
    left: 0,
    right: 0,
    borderRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    backgroundColor: theme.colors.cardSurface,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 6,
    elevation: 6,
    zIndex: 20,
  },
  accessDropdownItem: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
  },
  accessDropdownItemActive: {
    backgroundColor: theme.colors.borderLight,
  },
  accessDropdownText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  accessDropdownTextActive: {
    color: theme.colors.white,
  },
  errorText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.status.failed,
    marginTop: theme.spacing.xs,
  },
  submitButton: {
    backgroundColor: '#d97706',
    borderRadius: theme.borderRadius.md,
    paddingVertical: theme.spacing.sm,
    alignItems: 'center',
    marginTop: theme.spacing.md,
  },
  submitText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
});

