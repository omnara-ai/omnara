import React from 'react';
import { 
  TouchableOpacity, 
  Text, 
  StyleSheet, 
  ActivityIndicator,
  View,
} from 'react-native';
import { Crown } from 'lucide-react-native';
import { theme } from '@/constants/theme';

interface PurchaseButtonProps {
  onPress: () => void;
  isLoading?: boolean;
  disabled?: boolean;
  variant?: 'primary' | 'secondary';
  title?: string;
}

export const PurchaseButton: React.FC<PurchaseButtonProps> = ({
  onPress,
  isLoading = false,
  disabled = false,
  variant = 'primary',
  title = 'Upgrade to Pro - $9/month',
}) => {
  const isPrimary = variant === 'primary';

  return (
    <TouchableOpacity
      style={[
        styles.button,
        isPrimary ? styles.primaryButton : styles.secondaryButton,
        (disabled || isLoading) && styles.disabledButton,
      ]}
      onPress={onPress}
      disabled={disabled || isLoading}
      activeOpacity={0.8}
    >
      {isLoading ? (
        <ActivityIndicator 
          size="small" 
          color={isPrimary ? theme.colors.white : theme.colors.primaryLight} 
        />
      ) : (
        <View style={styles.content}>
          {isPrimary && (
            <Crown 
              size={20} 
              color={theme.colors.white} 
              strokeWidth={2} 
              style={styles.icon}
            />
          )}
          <Text style={[
            styles.text,
            isPrimary ? styles.primaryText : styles.secondaryText
          ]}>
            {title}
          </Text>
        </View>
      )}
    </TouchableOpacity>
  );
};

const styles = StyleSheet.create({
  button: {
    paddingVertical: theme.spacing.md,
    paddingHorizontal: theme.spacing.lg,
    borderRadius: theme.borderRadius.md,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 48,
  },
  primaryButton: {
    backgroundColor: theme.colors.proDark,
  },
  secondaryButton: {
    backgroundColor: 'transparent',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  disabledButton: {
    opacity: 0.5,
  },
  content: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  icon: {
    marginRight: theme.spacing.sm,
  },
  text: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
  },
  primaryText: {
    color: theme.colors.white,
  },
  secondaryText: {
    color: theme.colors.primaryLight,
  },
});