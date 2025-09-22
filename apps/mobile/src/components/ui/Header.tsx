import React from 'react';
import { View, Text, StyleSheet, ViewStyle } from 'react-native';
import { theme } from '@/constants/theme';
import { BackButton } from './BackButton';

interface HeaderProps {
  title: string;
  onBack?: () => void;
  rightContent?: React.ReactNode;
  leftContent?: React.ReactNode;
  centerContent?: React.ReactNode;
  style?: ViewStyle;
  showBorder?: boolean;
}

export const Header: React.FC<HeaderProps> = ({
  title,
  onBack,
  rightContent,
  leftContent,
  centerContent,
  style,
  showBorder = true,
}) => {
  const headerStyle = [
    styles.header,
    showBorder && styles.headerBorder,
    style,
  ];

  return (
    <View style={headerStyle}>
      {/* Left content */}
      <View style={styles.headerSide}>
        {leftContent || (onBack && <BackButton onPress={onBack} />)}
      </View>

      {/* Center content */}
      <View style={styles.headerCenter}>
        {centerContent || <Text style={styles.title}>{title}</Text>}
      </View>

      {/* Right content */}
      <View style={styles.headerSide}>
        {rightContent}
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.xs,
    backgroundColor: theme.colors.background,
  },
  headerBorder: {
    borderBottomWidth: 1,
    borderBottomColor: theme.colors.borderDivider,
  },
  headerSide: {
    width: 32, // Standard width for side elements
    justifyContent: 'center',
    alignItems: 'center',
  },
  headerCenter: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
  },
  title: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
});