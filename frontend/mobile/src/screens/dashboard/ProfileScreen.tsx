import React, { useState } from 'react';
import { 
  View, 
  Text, 
  StyleSheet, 
  Alert,
  ScrollView,
  TouchableOpacity,
  Modal,
  ActivityIndicator,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { ChevronRight, Key, LogOut, User, Bell, Trash2, AlertTriangle } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { Gradient, Card } from '@/components/ui';
import { LinearGradient } from 'expo-linear-gradient';
import { useAuth } from '@/contexts/AuthContext';
import { dashboardApi } from '@/services/api';

export const ProfileScreen: React.FC = () => {
  console.log('[ProfileScreen] Component rendered');
  
  const { user, profile, signOut } = useAuth();
  const navigation = useNavigation();
  const [showDeleteModal, setShowDeleteModal] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  const handleLogout = async () => {
    Alert.alert(
      'Logout',
      'Are you sure you want to logout?',
      [
        { text: 'Cancel', style: 'cancel' },
        { 
          text: 'Logout', 
          style: 'destructive',
          onPress: async () => {
            try {
              await signOut();
            } catch (error) {
              Alert.alert('Error', 'Failed to logout. Please try again.');
            }
          }
        }
      ]
    );
  };

  const navigateToAPIKeys = () => {
    navigation.navigate('APIKeys' as never);
  };

  const navigateToNotificationSettings = () => {
    navigation.navigate('NotificationSettings' as never);
  };

  const handleDeleteAccount = async () => {
    setIsDeleting(true);
    try {
      const response = await dashboardApi.deleteAccount();
      
      // Check if it was a partial success (status code 207)
      if (response.status_code === 207) {
        Alert.alert(
          'Partial Success',
          response.message,
          [{ text: 'OK', onPress: () => signOut() }]
        );
      } else {
        // Full success
        Alert.alert(
          'Account Deleted',
          'Your account has been successfully deleted.',
          [{ text: 'OK', onPress: () => signOut() }]
        );
      }
    } catch (error) {
      Alert.alert(
        'Error',
        error instanceof Error ? error.message : 'Failed to delete account. Please try again.',
      );
    } finally {
      setIsDeleting(false);
      setShowDeleteModal(false);
    }
  };

  return (
    <Gradient variant="dark" style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        <ScrollView 
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
        >
          <View style={styles.header}>
            <Text style={styles.title}>Profile</Text>
            <Text style={styles.subtitle}>Manage your account and settings</Text>
          </View>
          
          {/* User Info Card */}
          <View style={styles.profileCardContainer}>
            <LinearGradient
              colors={[withAlpha(theme.colors.primary, 0.12), withAlpha(theme.colors.primary, 0.06)]}
              start={{ x: 0, y: 0 }}
              end={{ x: 0, y: 1 }}
              style={styles.profileCard}
            >
              <View style={styles.avatarContainer}>
                <View style={styles.avatar}>
                  <User size={40} color={theme.colors.white} strokeWidth={1.5} />
                </View>
              </View>
              <Text style={styles.displayName}>
                {profile?.display_name || 'User'}
              </Text>
              <Text style={styles.email}>{user?.email || 'Not available'}</Text>
            </LinearGradient>
          </View>

          {/* Menu Items Card */}
          <View style={styles.menuCardContainer}>
            <LinearGradient
              colors={[withAlpha(theme.colors.primary, 0.12), withAlpha(theme.colors.primary, 0.06)]}
              start={{ x: 0, y: 0 }}
              end={{ x: 0, y: 1 }}
              style={styles.menuCard}
            >
              <TouchableOpacity 
                style={styles.menuItem}
                onPress={navigateToNotificationSettings}
                activeOpacity={0.7}
              >
                <View style={styles.menuLeft}>
                  <View style={styles.menuIconContainer}>
                    <Bell size={20} color={theme.colors.primaryLight} strokeWidth={2} />
                  </View>
                  <View>
                    <Text style={styles.menuTitle}>Notifications</Text>
                    <Text style={styles.menuSubtitle}>Manage notification settings</Text>
                  </View>
                </View>
                <ChevronRight size={20} color="rgba(255, 255, 255, 0.3)" />
              </TouchableOpacity>
              
              <View style={styles.menuDivider} />
              
              <TouchableOpacity 
                style={styles.menuItem}
                onPress={navigateToAPIKeys}
                activeOpacity={0.7}
              >
                <View style={styles.menuLeft}>
                  <View style={styles.menuIconContainer}>
                    <Key size={20} color={theme.colors.primaryLight} strokeWidth={2} />
                  </View>
                  <View>
                    <Text style={styles.menuTitle}>API Keys</Text>
                    <Text style={styles.menuSubtitle}>Manage your API access tokens</Text>
                  </View>
                </View>
                <ChevronRight size={20} color="rgba(255, 255, 255, 0.3)" />
              </TouchableOpacity>
            </LinearGradient>
          </View>

          {/* Account Info */}
          <View style={styles.infoCardContainer}>
            <LinearGradient
              colors={[withAlpha(theme.colors.primary, 0.12), withAlpha(theme.colors.primary, 0.06)]}
              start={{ x: 0, y: 0 }}
              end={{ x: 0, y: 1 }}
              style={styles.infoCard}
            >
              <Text style={styles.sectionTitle}>Account Information</Text>
              
              <View style={styles.infoRow}>
                <Text style={styles.infoLabel}>User ID</Text>
                <Text style={styles.infoValue} numberOfLines={1}>
                  {user?.id || 'Not available'}
                </Text>
              </View>
              
              <View style={[styles.infoRow, { borderBottomWidth: 0 }]}>
                <Text style={styles.infoLabel}>Member Since</Text>
                <Text style={styles.infoValue}>
                  {profile?.created_at 
                    ? new Date(profile.created_at).toLocaleDateString()
                    : 'Not available'}
                </Text>
              </View>
            </LinearGradient>
          </View>

          {/* Action Buttons */}
          <View style={styles.actionButtonsContainer}>
            {/* Sign Out Button */}
            <TouchableOpacity 
              style={styles.signOutButton}
              onPress={handleLogout}
              activeOpacity={0.8}
            >
              <LogOut size={20} color={theme.colors.white} strokeWidth={2} />
              <Text style={styles.signOutText}>Sign Out</Text>
            </TouchableOpacity>

            {/* Delete Account Button */}
            <TouchableOpacity 
              style={styles.deleteButton}
              onPress={() => setShowDeleteModal(true)}
              activeOpacity={0.8}
            >
              <Trash2 size={20} color={theme.colors.white} strokeWidth={2} />
              <Text style={styles.deleteText}>Delete Account</Text>
            </TouchableOpacity>
          </View>
        </ScrollView>
      </SafeAreaView>

      {/* Delete Account Confirmation Modal */}
      <Modal
        visible={showDeleteModal}
        transparent
        animationType="fade"
        onRequestClose={() => setShowDeleteModal(false)}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalIcon}>
              <AlertTriangle size={48} color={theme.colors.error} strokeWidth={1.5} />
            </View>
            
            <Text style={styles.modalTitle}>Delete Account</Text>
            <Text style={styles.modalMessage}>
              This action is permanent and cannot be undone. All your data, including agents, API keys, and settings will be permanently deleted.
            </Text>
            
            <Text style={styles.modalWarning}>
              Are you absolutely sure you want to delete your account?
            </Text>

            <View style={styles.modalButtons}>
              <TouchableOpacity
                style={[styles.modalButton, styles.cancelButton]}
                onPress={() => setShowDeleteModal(false)}
                disabled={isDeleting}
              >
                <Text style={styles.cancelButtonText}>Cancel</Text>
              </TouchableOpacity>
              
              <TouchableOpacity
                style={[styles.modalButton, styles.confirmDeleteButton]}
                onPress={handleDeleteAccount}
                disabled={isDeleting}
              >
                {isDeleting ? (
                  <ActivityIndicator size="small" color={theme.colors.white} />
                ) : (
                  <Text style={styles.confirmDeleteText}>Delete Account</Text>
                )}
              </TouchableOpacity>
            </View>
          </View>
        </View>
      </Modal>
    </Gradient>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  safeArea: {
    flex: 1,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingBottom: theme.spacing.xl * 3,
  },
  header: {
    paddingHorizontal: theme.spacing.lg,
    paddingTop: theme.spacing.lg,
    paddingBottom: theme.spacing.xl,
  },
  title: {
    fontSize: theme.fontSize['3xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  subtitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.primaryLight,
  },
  profileCardContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    overflow: 'hidden',
  },
  profileCard: {
    alignItems: 'center',
    paddingVertical: theme.spacing.xl,
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
  },
  avatarContainer: {
    marginBottom: theme.spacing.md,
  },
  avatar: {
    width: 80,
    height: 80,
    borderRadius: theme.borderRadius.full,
    backgroundColor: withAlpha(theme.colors.primary, 0.2),
    borderWidth: 2,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    justifyContent: 'center',
    alignItems: 'center',
  },
  displayName: {
    fontSize: theme.fontSize['2xl'],
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  email: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  menuCardContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    overflow: 'hidden',
  },
  menuCard: {
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
  },
  menuItem: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: theme.spacing.md,
  },
  menuDivider: {
    height: 1,
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    marginVertical: theme.spacing.xs,
  },
  menuLeft: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  menuIconContainer: {
    width: 40,
    height: 40,
    borderRadius: theme.borderRadius.md,
    backgroundColor: withAlpha(theme.colors.primary, 0.1),
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: theme.spacing.md,
  },
  menuTitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs / 2,
  },
  menuSubtitle: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  infoCardContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    overflow: 'hidden',
  },
  infoCard: {
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
  },
  sectionTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.md,
  },
  infoRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingVertical: theme.spacing.md,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(255, 255, 255, 0.1)',
  },
  infoLabel: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  infoValue: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
    maxWidth: '60%',
  },
  actionButtonsContainer: {
    paddingHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
  },
  signOutButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: theme.colors.primary,
    paddingVertical: theme.spacing.md,
    paddingHorizontal: theme.spacing.lg,
    borderRadius: theme.borderRadius.md,
    marginBottom: theme.spacing.xl * 2,
  },
  signOutText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginLeft: theme.spacing.sm,
  },
  deleteButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: theme.colors.error,
    paddingVertical: theme.spacing.md,
    paddingHorizontal: theme.spacing.lg,
    borderRadius: theme.borderRadius.md,
  },
  deleteText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginLeft: theme.spacing.sm,
  },
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.8)',
    justifyContent: 'center',
    alignItems: 'center',
    padding: theme.spacing.lg,
  },
  modalContent: {
    backgroundColor: theme.colors.background,
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.xl,
    width: '100%',
    maxWidth: 400,
    borderWidth: 1,
    borderColor: 'rgba(239, 68, 68, 0.3)',
  },
  modalIcon: {
    alignItems: 'center',
    marginBottom: theme.spacing.lg,
  },
  modalTitle: {
    fontSize: theme.fontSize['2xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    textAlign: 'center',
    marginBottom: theme.spacing.md,
  },
  modalMessage: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.8)',
    textAlign: 'center',
    marginBottom: theme.spacing.md,
    lineHeight: 22,
  },
  modalWarning: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.error,
    textAlign: 'center',
    marginBottom: theme.spacing.xl,
  },
  modalButtons: {
    flexDirection: 'row',
    gap: theme.spacing.md,
  },
  modalButton: {
    flex: 1,
    paddingVertical: theme.spacing.md,
    borderRadius: theme.borderRadius.md,
    alignItems: 'center',
    justifyContent: 'center',
  },
  cancelButton: {
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  cancelButtonText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  confirmDeleteButton: {
    backgroundColor: theme.colors.error,
  },
  confirmDeleteText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
});
