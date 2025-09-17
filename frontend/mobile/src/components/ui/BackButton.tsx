import React from 'react';
import { TouchableOpacity, StyleSheet, TouchableOpacityProps } from 'react-native';
import { ChevronLeft } from 'lucide-react-native';
import { theme } from '@/constants/theme';

interface BackButtonProps extends Omit<TouchableOpacityProps, 'style'> {
  size?: 'small' | 'medium';
  color?: string;
}

export const BackButton: React.FC<BackButtonProps> = ({ 
  size = 'medium',
  color = theme.colors.white,
  onPress,
  ...props 
}) => {
  const buttonSize = size === 'small' ? styles.small : styles.medium;
  const iconSize = size === 'small' ? 18 : 24;

  return (
    <TouchableOpacity 
      style={[styles.base, buttonSize]}
      onPress={onPress}
      activeOpacity={0.7}
      {...props}
    >
      <ChevronLeft size={iconSize} color={color} strokeWidth={2} />
    </TouchableOpacity>
  );
};

const styles = StyleSheet.create({
  base: {
    justifyContent: 'center',
    alignItems: 'center',
  },
  small: {
    width: 28,
    height: 28,
  },
  medium: {
    width: 32,
    height: 32,
  },
});