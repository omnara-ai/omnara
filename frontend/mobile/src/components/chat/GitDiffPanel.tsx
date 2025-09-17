import React, { useMemo } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  LayoutAnimation,
  UIManager,
  Platform,
  Dimensions,
} from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { parseGitDiff, DiffFile } from '@/utils/gitDiffParser';

// Enable LayoutAnimation on Android
if (Platform.OS === 'android' && UIManager.setLayoutAnimationEnabledExperimental) {
  UIManager.setLayoutAnimationEnabledExperimental(true);
}

interface GitDiffPanelProps {
  gitDiff: string | null | undefined;
  hasInputBelow?: boolean;
  isCompleted?: boolean;
  onFilePress?: (gitDiff: string, fileIndex: number) => void;
}

interface DiffLine {
  type: 'header' | 'file' | 'hunk' | 'addition' | 'deletion' | 'context';
  content: string;
}

function parseDiffLines(diff: string): DiffLine[] {
  const lines = diff.split('\n');
  const parsedLines: DiffLine[] = [];
  
  lines.forEach((line) => {
    if (line.startsWith('diff --git')) {
      parsedLines.push({ type: 'header', content: line });
    } else if (line.startsWith('index ') || line.startsWith('new file mode') || line.startsWith('deleted file mode')) {
      parsedLines.push({ type: 'file', content: line });
    } else if (line.startsWith('---') || line.startsWith('+++')) {
      parsedLines.push({ type: 'file', content: line });
    } else if (line.startsWith('@@')) {
      parsedLines.push({ type: 'hunk', content: line });
    } else if (line.startsWith('+')) {
      parsedLines.push({ type: 'addition', content: line });
    } else if (line.startsWith('-')) {
      parsedLines.push({ type: 'deletion', content: line });
    } else {
      parsedLines.push({ type: 'context', content: line });
    }
  });
  
  return parsedLines;
}

export const GitDiffPanel: React.FC<GitDiffPanelProps> = ({ gitDiff, hasInputBelow = true, isCompleted = false, onFilePress }) => {
  const diffSummary = useMemo(() => parseGitDiff(gitDiff), [gitDiff]);
  
  if (!diffSummary || diffSummary.files.length === 0) {
    return null;
  }

  const openDiffModal = (fileIndex: number) => {
    if (onFilePress && gitDiff) {
      onFilePress(gitDiff, fileIndex);
    }
  };

  return (
    <View style={[styles.container, isCompleted && !hasInputBelow && styles.containerCompleted]}>
      {/* File tabs */}
      <View style={styles.header}>
        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          contentContainerStyle={styles.fileTabsContent}
          style={styles.fileTabsScroll}
        >
          {diffSummary.files.map((file, index) => {
            const filename = file.filename.split('/').pop() || file.filename;
            
            return (
              <TouchableOpacity
                key={file.filename}
                onPress={() => openDiffModal(index)}
                style={styles.fileTab}
                activeOpacity={0.7}
              >
                <Text style={styles.fileIcon}>📄</Text>
                <Text style={styles.fileName}>
                  {filename}
                </Text>
                <View style={styles.statsContainer}>
                  <Text style={styles.additionsStat}>+{file.additions}</Text>
                  <Text style={styles.deletionsStat}>-{file.deletions}</Text>
                </View>
              </TouchableOpacity>
            );
          })}
        </ScrollView>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: theme.spacing.md,
  },
  containerCompleted: {
    marginBottom: theme.spacing.xl,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: theme.spacing.xs,
    paddingHorizontal: theme.spacing.md,
  },
  fileTabsScroll: {
    flex: 1,
  },
  fileTabsContent: {
    paddingRight: theme.spacing.md,
    gap: theme.spacing.xs,
  },
  fileTab: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: theme.colors.glass.white,
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: 6,
    borderRadius: 16,
    marginRight: theme.spacing.xs,
    borderWidth: 1,
    borderColor: theme.colors.glass.white,
  },
  fileIcon: {
    fontSize: 11,
    marginRight: 6,
  },
  fileName: {
    fontSize: 12,
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.textMuted,
    marginRight: theme.spacing.xs,
  },
  statsContainer: {
    flexDirection: 'row',
    gap: 4,
  },
  additionsStat: {
    fontSize: 11,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.successLight,
  },
  deletionsStat: {
    fontSize: 11,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.errorLight,
  },
});