import React, { useState, useMemo } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
} from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { parseGitDiff } from '@/utils/gitDiffParser';
import { BottomSheetDiffViewer } from './BottomSheetDiffViewer';

interface MobileDiffViewerProps {
  gitDiff: string | null | undefined;
  hasInputBelow?: boolean;
  isCompleted?: boolean;
}

export const MobileDiffViewer: React.FC<MobileDiffViewerProps> = ({ 
  gitDiff, 
  hasInputBelow = true, 
  isCompleted = false 
}) => {
  const diffSummary = useMemo(() => parseGitDiff(gitDiff), [gitDiff]);
  const [showModal, setShowModal] = useState(false);
  
  if (!diffSummary || diffSummary.files.length === 0) {
    return null;
  }

  return (
    <>
      <View style={[styles.container, isCompleted && !hasInputBelow && styles.containerCompleted]}>
        <TouchableOpacity 
          onPress={() => setShowModal(true)}
          style={styles.compactContainer}
          activeOpacity={0.7}
        >
          <View style={styles.compactContent}>
            <View style={styles.compactLeft}>
              <Text style={styles.compactIcon}>↗</Text>
              <Text style={styles.compactFileCount}>{diffSummary.files.length} files</Text>
              <View style={styles.compactDivider} />
              <Text style={styles.compactAdditions}>+{diffSummary.totalAdditions}</Text>
              <Text style={styles.compactDeletions}>-{diffSummary.totalDeletions}</Text>
            </View>
            <View style={styles.compactRight}>
              <Text style={styles.compactAction}>Review</Text>
              <Text style={styles.compactArrow}>›</Text>
            </View>
          </View>
        </TouchableOpacity>
      </View>
      
      <BottomSheetDiffViewer
        visible={showModal}
        onClose={() => setShowModal(false)}
        gitDiff={gitDiff}
      />
    </>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: theme.spacing.md,
    marginVertical: theme.spacing.sm,
  },
  containerCompleted: {
    marginBottom: theme.spacing.xl,
  },
  compactContainer: {
    backgroundColor: 'rgba(25, 25, 30, 0.98)',
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.06)',
    overflow: 'hidden',
  },
  compactContent: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: 6,
    paddingHorizontal: theme.spacing.md,
  },
  compactLeft: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
    gap: theme.spacing.sm,
  },
  compactIcon: {
    fontSize: 14,
    color: theme.colors.textMuted,
    opacity: 0.8,
  },
  compactFileCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.semibold,
    color: theme.colors.white,
  },
  compactAdditions: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.successLight,
  },
  compactDeletions: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.errorLight,
  },
  compactRight: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingLeft: theme.spacing.md,
  },
  compactAction: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.white,
    marginRight: 4,
  },
  compactArrow: {
    fontSize: 18,
    color: theme.colors.textMuted,
    opacity: 0.6,
  },
  compactDivider: {
    width: 1,
    height: 12,
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    marginHorizontal: theme.spacing.sm,
  },
});