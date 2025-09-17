import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  ScrollView,
  Alert,
  Modal,
  ActivityIndicator,
  Platform,
} from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { DrawerContentScrollView } from '@react-navigation/drawer';
import { useNavigation } from '@react-navigation/native';
import { 
  User, 
  Key, 
  Bell, 
  Shield, 
  LogOut, 
  Trash2, 
  ChevronRight,
  AlertTriangle,
  CreditCard,
  Terminal 
} from 'lucide-react-native';
import { BlurView } from 'expo-blur';
import { useAuth } from '@/contexts/AuthContext';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { dashboardApi } from '@/services/api';

interface MenuItemProps {
  icon: React.ComponentType<any>;
  title: string;
  subtitle?: string;
  onPress: () => void;
  variant?: 'default' | 'danger';
}

const MenuItem: React.FC<MenuItemProps> = ({ 
  icon: Icon, 
  title, 
  subtitle, 
  onPress, 
  variant = 'default' 
}) => {
  const isDestructive = variant === 'danger';
  
  return (
    <TouchableOpacity
      style={styles.menuItem}
      onPress={onPress}
      activeOpacity={0.7}
    >
      <Icon 
        size={20} 
        color={isDestructive ? theme.colors.error : theme.colors.primaryLight} 
        strokeWidth={1.5} 
        style={styles.menuIcon}
      />
      <View style={styles.menuTextContainer}>
        <Text style={[
          styles.menuTitle,
          isDestructive && styles.menuTitleDanger
        ]}>
          {title}
        </Text>
        {subtitle && <Text style={styles.menuSubtitle}>{subtitle}</Text>}
      </View>
      <ChevronRight size={18} color="rgba(255, 255, 255, 0.3)" />
    </TouchableOpacity>
  );
};

export const ProfileSidebar: React.FC<any> = (props) => {
  const { user, profile, signOut } = useAuth();
  const navigation = useNavigation();
  const insets = useSafeAreaInsets();
  const [showDeleteModal, setShowDeleteModal] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  const handleNavigate = (screen: string) => {
    navigation.navigate(screen as never);
    props.navigation.closeDrawer();
  };

  const handleLogout = async () => {
    Alert.alert(
      'Sign Out',
      'Are you sure you want to sign out?',
      [
        { text: 'Cancel', style: 'cancel' },
        { 
          text: 'Sign Out', 
          style: 'destructive',
          onPress: async () => {
            try {
              await signOut();
            } catch (error) {
              Alert.alert('Error', 'Failed to sign out. Please try again.');
            }
          }
        }
      ]
    );
  };

  const handleDeleteAccount = async () => {
    setIsDeleting(true);
    try {
      const response = await dashboardApi.deleteAccount();
      
      if (response.status_code === 207) {
        Alert.alert(
          'Partial Success',
          response.message,
          [{ text: 'OK', onPress: () => signOut() }]
        );
      } else {
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
    <View style={styles.container}>
      <DrawerContentScrollView 
        {...props} 
        contentContainerStyle={[styles.scrollContent, { paddingTop: insets.top + 20, paddingBottom: insets.bottom + 20 }]}
        showsVerticalScrollIndicator={false}
      >
        {/* Profile Section */}
        <View style={styles.profileSection}>
          <View style={styles.avatarContainer}>
            <View style={styles.avatar}>
              <User size={32} color={theme.colors.white} strokeWidth={1.5} />
            </View>
          </View>
          <Text style={styles.displayName}>
            {profile?.display_name || 'User'}
          </Text>
          <Text style={styles.email}>{user?.email || 'Not available'}</Text>
        </View>

        {/* Menu Section */}
        <View style={styles.menuSection}>
          <MenuItem
            icon={Terminal}
            title="Setup Guide"
            subtitle="Review onboarding and setup steps"
            onPress={() => handleNavigate('Onboarding')}
          />
          <View style={styles.menuDivider} />
          
          <MenuItem
            icon={Key}
            title="API Keys"
            subtitle="Manage your API access tokens"
            onPress={() => handleNavigate('APIKeys')}
          />
          <View style={styles.menuDivider} />
          
          <MenuItem
            icon={Bell}
            title="Notifications"
            subtitle="Configure notification settings"
            onPress={() => handleNavigate('NotificationSettings')}
          />
          
          {Platform.OS === 'ios' && (
            <>
              <View style={styles.menuDivider} />
              <MenuItem
                icon={CreditCard}
                title="Subscription"
                subtitle="Manage your Pro subscription"
                onPress={() => handleNavigate('Subscription')}
              />
            </>
          )}
          
          <View style={styles.menuDivider} />
          <MenuItem
            icon={Shield}
            title="Legal"
            subtitle="Privacy & Terms"
            onPress={() => handleNavigate('Legal')}
          />
        </View>

        {/* Account Actions */}
        <View style={styles.accountSection}>
          <MenuItem
            icon={LogOut}
            title="Sign Out"
            onPress={handleLogout}
          />
          
          <View style={styles.buttonSpacer} />
          
          <MenuItem
            icon={Trash2}
            title="Delete Account"
            variant="danger"
            onPress={() => setShowDeleteModal(true)}
          />
        </View>
      </DrawerContentScrollView>

      {/* Delete Account Modal */}
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
              This action is permanent and cannot be undone. All your data will be permanently deleted.
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
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.colors.background,
  },
  scrollContent: {
    flexGrow: 1,
  },
  profileSection: {
    alignItems: 'center',
    paddingHorizontal: theme.spacing.lg,
    paddingBottom: theme.spacing.xl,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(255, 255, 255, 0.1)',
  },
  avatarContainer: {
    marginBottom: theme.spacing.md,
  },
  avatar: {
    width: 72,
    height: 72,
    borderRadius: theme.borderRadius.full,
    backgroundColor: withAlpha(theme.colors.primary, 0.15),
    borderWidth: 2,
    borderColor: withAlpha(theme.colors.primary, 0.3),
    justifyContent: 'center',
    alignItems: 'center',
  },
  displayName: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs / 2,
  },
  email: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  menuSection: {
    paddingTop: theme.spacing.lg,
  },
  accountSection: {
    marginTop: theme.spacing.xl,
    marginBottom: theme.spacing.xl,
    paddingTop: theme.spacing.lg,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  menuItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
  },
  menuIcon: {
    marginRight: theme.spacing.md,
  },
  menuTextContainer: {
    flex: 1,
  },
  menuTitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    fontWeight: theme.fontWeight.normal as any,
    color: theme.colors.white,
    marginBottom: 2,
  },
  menuTitleDanger: {
    color: theme.colors.error,
  },
  menuSubtitle: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
  },
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.8)',
    justifyContent: 'center',
    alignItems: 'center',
    padding: theme.spacing.lg,
  },
  modalContent: {
    backgroundColor: theme.colors.authContainer,
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
    minHeight: 44,
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
  menuDivider: {
    height: 1,
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    marginLeft: 44, // Align with text (icon width + margin)
  },
  buttonSpacer: {
    height: theme.spacing.xl * 2,
  },
});
