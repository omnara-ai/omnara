import React, { useState, useRef, useMemo, useCallback, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Modal,
  Dimensions,
  PanResponder,
  Animated,
  
  FlatList,
} from 'react-native';
import { useSafeAreaInsets, SafeAreaView } from 'react-native-safe-area-context';
import { theme } from '@/constants/theme';
import { parseGitDiff, DiffFile } from '@/utils/gitDiffParser';

const { height: SCREEN_HEIGHT, width: SCREEN_WIDTH } = Dimensions.get('window');

// Syntax highlighting patterns
const SYNTAX_PATTERNS = {
  string: /(['"`])(?:(?=(\\?))\2.)*?\1/g,
  number: /\b\d+\.?\d*\b/g,
  keyword: /\b(const|let|var|function|return|if|else|for|while|class|interface|type|import|export|from|extends|implements|async|await|try|catch|throw|new|this|super|null|undefined|true|false|def|elif|lambda|with|as|pass|break|continue|yield)\b/g,
  comment: /(\/\/.*$|\/\*[\s\S]*?\*\/|#.*$)/gm,
  property: /(\w+)(?=:)/g,
  function: /(\w+)(?=\()/g,
  operator: /(\+|-|\*|\/|=|==|===|!=|!==|<|>|<=|>=|&&|\|\||!|\?|:|%|\+=|-=|\*=|\/=|=>)/g,
};

// Color theme for syntax highlighting
const SYNTAX_COLORS = {
  string: '#98C379',      // Green
  number: '#56B6C2',      // Cyan
  keyword: '#C678DD',     // Purple
  comment: '#5C6370',     // Gray
  property: '#E06C75',    // Red/Pink
  function: '#E5C07B',    // Yellow
  operator: '#ABB2BF',    // Light gray
  default: theme.colors.white,
};

interface BottomSheetDiffViewerProps {
  visible: boolean;
  onClose: () => void;
  gitDiff: string | null | undefined;
  initialFileIndex?: number;
}

interface DiffHunk {
  type: 'header' | 'hunk' | 'collapsed' | 'line';
  content: string;
  startLine?: number;
  endLine?: number;
  lines?: DiffLine[];
  isExpanded?: boolean;
  hunkIndex?: number;
}

interface DiffLine {
  type: 'addition' | 'deletion' | 'context';
  content: string;
  oldLineNumber?: number;
  newLineNumber?: number;
}

function parseSmartDiff(content: string): DiffHunk[] {
  const lines = content.split('\n');
  const hunks: DiffHunk[] = [];
  let currentHunk: DiffLine[] = [];
  let inHunk = false;
  let contextBuffer: DiffLine[] = [];
  let oldLineNum = 0;
  let newLineNum = 0;
  let hunkIndex = 0;
  
  const CONTEXT_SIZE = 3;
  
  lines.forEach((line, index) => {
    if (line.startsWith('diff --git')) {
      if (currentHunk.length > 0) {
        hunks.push({
          type: 'hunk',
          content: '',
          lines: currentHunk,
          hunkIndex: hunkIndex++,
        });
        currentHunk = [];
      }
      hunks.push({ type: 'header', content: line });
      inHunk = false;
    } else if (line.startsWith('@@')) {
      const match = line.match(/@@ -(\d+),?\d* \+(\d+),?\d* @@/);
      if (match) {
        oldLineNum = parseInt(match[1]);
        newLineNum = parseInt(match[2]);
      }
      
      if (currentHunk.length > 0) {
        hunks.push({
          type: 'hunk',
          content: '',
          lines: currentHunk,
          hunkIndex: hunkIndex++,
        });
        currentHunk = [];
      }
      
      hunks.push({ type: 'header', content: line });
      inHunk = true;
      contextBuffer = [];
    } else if (line.startsWith('+') && !line.startsWith('+++')) {
      const diffLine: DiffLine = {
        type: 'addition',
        content: line.substring(1),
        newLineNumber: newLineNum++,
      };
      
      if (contextBuffer.length > 0) {
        currentHunk.push(...contextBuffer.slice(-CONTEXT_SIZE));
        contextBuffer = [];
      }
      
      currentHunk.push(diffLine);
      inHunk = true;
    } else if (line.startsWith('-') && !line.startsWith('---')) {
      const diffLine: DiffLine = {
        type: 'deletion',
        content: line.substring(1),
        oldLineNumber: oldLineNum++,
      };
      
      if (contextBuffer.length > 0) {
        currentHunk.push(...contextBuffer.slice(-CONTEXT_SIZE));
        contextBuffer = [];
      }
      
      currentHunk.push(diffLine);
      inHunk = true;
    } else if (!line.startsWith('\\')) {
      const diffLine: DiffLine = {
        type: 'context',
        content: line,
        oldLineNumber: oldLineNum++,
        newLineNumber: newLineNum++,
      };
      
      if (inHunk) {
        currentHunk.push(diffLine);
        if (currentHunk.filter(l => l.type !== 'context').length === 0) {
          contextBuffer.push(diffLine);
        }
      } else {
        contextBuffer.push(diffLine);
      }
      
      // Check if we've accumulated enough context to create a collapsed section
      if (contextBuffer.length > CONTEXT_SIZE * 2 && !inHunk) {
        const collapsedCount = contextBuffer.length - CONTEXT_SIZE;
        if (collapsedCount > 5) {
          hunks.push({
            type: 'collapsed',
            content: `${collapsedCount} unchanged lines`,
            startLine: contextBuffer[0].oldLineNumber,
            endLine: contextBuffer[contextBuffer.length - 1].oldLineNumber,
            lines: contextBuffer,
            isExpanded: false,
          });
          contextBuffer = [];
        }
      }
    }
  });
  
  if (currentHunk.length > 0) {
    hunks.push({
      type: 'hunk',
      content: '',
      lines: currentHunk.slice(0, currentHunk.length - Math.min(CONTEXT_SIZE, currentHunk.filter(l => l.type === 'context').length)),
      hunkIndex: hunkIndex++,
    });
  }
  
  return hunks;
}

// Get file extension from filename
const getFileExtension = (filename: string): string => {
  const parts = filename.split('.');
  return parts.length > 1 ? parts[parts.length - 1].toLowerCase() : '';
};

// Detect if file type supports syntax highlighting
const shouldHighlight = (filename: string): boolean => {
  const ext = getFileExtension(filename);
  const supportedExtensions = ['js', 'jsx', 'ts', 'tsx', 'py', 'java', 'cpp', 'c', 'h', 'cs', 'rb', 'go', 'rs', 'swift', 'kt', 'php', 'sh', 'bash', 'json', 'xml', 'html', 'css', 'scss', 'sass', 'less'];
  return supportedExtensions.includes(ext);
};

// Create syntax highlighted JSX
const highlightSyntax = (text: string, filename?: string, fontSize: number = 12): React.ReactNode => {
  const baseStyle = {
    fontFamily: theme.fontFamily.mono,
    fontSize,
    paddingVertical: 2,
  };

  if (!filename || !shouldHighlight(filename)) {
    return <Text style={[baseStyle, { color: theme.colors.white }]}>{text}</Text>;
  }

  // Create a map of all matches with their positions and types
  const matches: Array<{ start: number; end: number; type: string; text: string }> = [];
  
  Object.entries(SYNTAX_PATTERNS).forEach(([type, pattern]) => {
    const regex = new RegExp(pattern);
    let match;
    while ((match = regex.exec(text)) !== null) {
      matches.push({
        start: match.index,
        end: match.index + match[0].length,
        type,
        text: match[0],
      });
    }
  });

  // Sort matches by position
  matches.sort((a, b) => a.start - b.start);

  // Remove overlapping matches (keep the first one)
  const filteredMatches = matches.filter((match, index) => {
    if (index === 0) return true;
    const prevMatch = matches[index - 1];
    return match.start >= prevMatch.end;
  });

  // Build the highlighted text
  const elements: React.ReactNode[] = [];
  let lastIndex = 0;

  filteredMatches.forEach((match, index) => {
    // Add text before the match
    if (match.start > lastIndex) {
      elements.push(
        <Text key={`text-${index}`} style={[baseStyle, { color: theme.colors.white }]}>
          {text.substring(lastIndex, match.start)}
        </Text>
      );
    }

    // Add the highlighted match
    elements.push(
      <Text
        key={`highlight-${index}`}
        style={[baseStyle, { color: SYNTAX_COLORS[match.type as keyof typeof SYNTAX_COLORS] || SYNTAX_COLORS.default }]}
      >
        {match.text}
      </Text>
    );

    lastIndex = match.end;
  });

  // Add remaining text
  if (lastIndex < text.length) {
    elements.push(
      <Text key="text-final" style={[baseStyle, { color: theme.colors.white }]}>
        {text.substring(lastIndex)}
      </Text>
    );
  }

  return <Text style={baseStyle}>{elements}</Text>;
};

export const BottomSheetDiffViewer: React.FC<BottomSheetDiffViewerProps> = ({
  visible,
  onClose,
  gitDiff,
  initialFileIndex = 0,
}) => {
  const insets = useSafeAreaInsets();
  // Restore prior top offset that looked good visually
  const TOP_OFFSET = Math.max(insets.top, 12) + theme.spacing.lg;
  const SHEET_HEIGHT = SCREEN_HEIGHT - TOP_OFFSET;
  
  // Bottom sheet snap points - computed from sheet height so bottom isn't cut off
  const SHEET_CLOSED = SHEET_HEIGHT + Math.max(insets.bottom, 16);
  const SHEET_HALF = SHEET_HEIGHT * 0.6; // ~40% visible
  const SHEET_FULL = 0; // translateY at fully open; height handles top spacing
  
  const diffSummary = useMemo(() => parseGitDiff(gitDiff), [gitDiff]);
  const [currentFileIndex, setCurrentFileIndex] = useState(initialFileIndex);
  const [showFileDrawer, setShowFileDrawer] = useState(false);
  const [expandedHunks, setExpandedHunks] = useState<Set<string>>(new Set());
  const [fontSize, setFontSize] = useState(12);
  
  const translateY = useRef(new Animated.Value(SHEET_CLOSED)).current;
  // Pinch-to-zoom handled by pinchResponder updating fontSize directly
  const currentSnapPoint = useRef(SHEET_CLOSED);
  const diffSummaryRef = useRef(diffSummary);
  
  // Update diffSummaryRef when diffSummary changes
  useEffect(() => {
    diffSummaryRef.current = diffSummary;
  }, [diffSummary]);

  // Update currentFileIndex when modal opens with a new initialFileIndex
  useEffect(() => {
    if (visible) {
      setCurrentFileIndex(initialFileIndex);
      // Animate to half-open state
      Animated.spring(translateY, {
        toValue: SHEET_HALF,
        useNativeDriver: true,
        tension: 50,
        friction: 10,
      }).start();
      currentSnapPoint.current = SHEET_HALF;
      
    } else {
      // Animate to closed
      Animated.timing(translateY, {
        toValue: SHEET_CLOSED,
        duration: 300,
        useNativeDriver: true,
      }).start();
      currentSnapPoint.current = SHEET_CLOSED;
    }
  }, [visible, initialFileIndex]);
  
  const currentFile = diffSummary?.files?.[currentFileIndex];
  const diffHunks = useMemo(() => {
    if (!currentFile) return [];
    return parseSmartDiff(currentFile.content);
  }, [currentFile]);
  
  const panResponder = useRef(
    PanResponder.create({
      // Claim touches immediately on designated drag regions
      onStartShouldSetPanResponder: () => true,
      onStartShouldSetPanResponderCapture: () => true,
      onMoveShouldSetPanResponder: () => true,
      onMoveShouldSetPanResponderCapture: () => true,
      onPanResponderMove: (_, gestureState) => {
        const newTranslateY = currentSnapPoint.current + gestureState.dy;
        // Prevent going above SHEET_FULL (safe area) and below SHEET_CLOSED
        if (newTranslateY >= SHEET_FULL && newTranslateY <= SHEET_CLOSED) {
          translateY.setValue(newTranslateY);
        }
      },
      onPanResponderRelease: (_, gestureState) => {
        const currentY = currentSnapPoint.current + gestureState.dy;
        const velocity = gestureState.vy;
        
        let snapTo = currentSnapPoint.current;
        
        // Determine snap point based on position and velocity (increased thresholds)
        if (velocity > 0.8) {
          // Fast downward swipe
          if (currentSnapPoint.current === SHEET_FULL) {
            snapTo = SHEET_HALF;
          } else {
            snapTo = SHEET_CLOSED;
          }
        } else if (velocity < -0.8) {
          // Fast upward swipe
          if (currentSnapPoint.current === SHEET_CLOSED) {
            snapTo = SHEET_HALF;
          } else if (currentSnapPoint.current === SHEET_HALF) {
            snapTo = SHEET_FULL;
          }
        } else {
          // Slow drag - find nearest snap point
          const distanceFromClosed = Math.abs(currentY - SHEET_CLOSED);
          const distanceFromHalf = Math.abs(currentY - SHEET_HALF);
          const distanceFromFull = Math.abs(currentY - SHEET_FULL);
          
          const minDistance = Math.min(distanceFromClosed, distanceFromHalf, distanceFromFull);
          
          if (minDistance === distanceFromClosed) {
            snapTo = SHEET_CLOSED;
          } else if (minDistance === distanceFromHalf) {
            snapTo = SHEET_HALF;
          } else {
            snapTo = SHEET_FULL;
          }
        }
        
        // Animate to snap point
        Animated.spring(translateY, {
          toValue: snapTo,
          useNativeDriver: true,
          tension: 50,
          friction: 10,
        }).start(() => {
          currentSnapPoint.current = snapTo;
          if (snapTo === SHEET_CLOSED) {
            onClose();
          }
        });
      },
    })
  ).current;
  
  const pinchResponder = useRef(
    PanResponder.create({
      onStartShouldSetPanResponder: (evt) => evt.nativeEvent.touches.length >= 2,
      onMoveShouldSetPanResponder: (evt) => evt.nativeEvent.touches.length >= 2,
      onPanResponderMove: (evt) => {
        if (evt.nativeEvent.touches.length >= 2) {
          const touch1 = evt.nativeEvent.touches[0];
          const touch2 = evt.nativeEvent.touches[1];
          const distance = Math.sqrt(
            Math.pow(touch2.pageX - touch1.pageX, 2) +
            Math.pow(touch2.pageY - touch1.pageY, 2)
          );
          
          const scale = distance / 300; // Base distance for scale
          const newFontSize = Math.max(8, Math.min(24, 12 * scale));
          setFontSize(newFontSize);
        }
      },
    })
  ).current;
  
  const navigateFile = (direction: 'prev' | 'next') => {
    const newIndex = direction === 'next' 
      ? Math.min(currentFileIndex + 1, diffSummaryRef.current.files.length - 1)
      : Math.max(currentFileIndex - 1, 0);
    
    if (newIndex !== currentFileIndex) {
      setCurrentFileIndex(newIndex);
    }
  };
  
  const toggleHunk = (hunkId: string) => {
    setExpandedHunks(prev => {
      const newSet = new Set(prev);
      if (newSet.has(hunkId)) {
        newSet.delete(hunkId);
      } else {
        newSet.add(hunkId);
      }
      return newSet;
    });
  };
  
  const renderDiffLine = (line: DiffLine, index: number) => (
    <View key={index} style={[
      styles.diffLineContainer,
      line.type === 'addition' && styles.additionLineBackground,
      line.type === 'deletion' && styles.deletionLineBackground,
    ]}>
      <View style={styles.gutter}>
        <Text style={[
          styles.lineNumber,
          line.type === 'deletion' && styles.deletionLineNumber,
        ]}>
          {line.oldLineNumber || ''}
        </Text>
        <Text style={[
          styles.lineNumber,
          line.type === 'addition' && styles.additionLineNumber,
        ]}>
          {line.newLineNumber || ''}
        </Text>
        <Text style={[
          styles.changeIndicator,
          line.type === 'addition' && styles.additionIndicator,
          line.type === 'deletion' && styles.deletionIndicator,
        ]}>
          {line.type === 'addition' ? '+' : line.type === 'deletion' ? '-' : ''}
        </Text>
      </View>
      {highlightSyntax(line.content, currentFile.filename, fontSize)}
    </View>
  );
  
  const renderHunk = (hunk: DiffHunk, index: number) => {
    if (hunk.type === 'header') {
      return (
        <Text key={index} style={styles.headerText}>
          {hunk.content}
        </Text>
      );
    }
    
    if (hunk.type === 'collapsed') {
      const hunkId = `${currentFileIndex}-${index}`;
      const isExpanded = expandedHunks.has(hunkId);
      
      return (
        <TouchableOpacity
          key={index}
          onPress={() => toggleHunk(hunkId)}
          style={styles.collapsedSection}
          activeOpacity={0.7}
        >
          <View style={styles.collapsedContent}>
            <Text style={styles.collapsedIcon}>
              {isExpanded ? '▼' : '▶'}
            </Text>
            <Text style={styles.collapsedText}>
              {hunk.content} (lines {hunk.startLine}-{hunk.endLine})
            </Text>
          </View>
          {isExpanded && hunk.lines && (
            <View>{hunk.lines.map((line, i) => renderDiffLine(line, i))}</View>
          )}
        </TouchableOpacity>
      );
    }
    
    if (hunk.type === 'hunk' && hunk.lines) {
      return (
        <ScrollView
          key={index}
          horizontal
          showsHorizontalScrollIndicator={true}
          style={styles.hunkScrollView}
        >
          <View style={styles.hunkContent}>
            {hunk.lines.map((line, i) => renderDiffLine(line, i))}
          </View>
        </ScrollView>
      );
    }
    
    return null;
  };
  
  const renderFileDrawer = () => (
    <Modal
      visible={showFileDrawer}
      transparent
      animationType="slide"
      onRequestClose={() => setShowFileDrawer(false)}
    >
      <TouchableOpacity 
        style={styles.drawerOverlay} 
        activeOpacity={1}
        onPress={() => setShowFileDrawer(false)}
      >
        <SafeAreaView edges={["bottom"]}>
          <View style={styles.fileDrawer}>
            <View style={styles.drawerHandle} />
            <Text style={styles.drawerTitle}>Files Changed</Text>
            <FlatList
              data={diffSummary.files}
              keyExtractor={(item) => item.filename}
              renderItem={({ item, index }) => (
                <TouchableOpacity
                  style={[
                    styles.fileDrawerItem,
                    index === currentFileIndex && styles.fileDrawerItemActive,
                  ]}
                  onPress={() => {
                    setCurrentFileIndex(index);
                    setShowFileDrawer(false);
                  }}
                >
                  <Text style={[
                    styles.fileDrawerItemText,
                    index === currentFileIndex && styles.fileDrawerItemTextActive,
                  ]}>
                    {item.filename}
                  </Text>
                  <View style={styles.fileDrawerStats}>
                    <Text style={styles.fileDrawerAdditions}>+{item.additions}</Text>
                    <Text style={styles.fileDrawerDeletions}>-{item.deletions}</Text>
                  </View>
                </TouchableOpacity>
              )}
            />
          </View>
        </SafeAreaView>
      </TouchableOpacity>
    </Modal>
  );
  
  // Early return checks must be after ALL hooks
  if (!diffSummary || diffSummary.files.length === 0) {
    return null;
  }
  
  if (!visible && currentSnapPoint.current === SHEET_CLOSED) {
    return null;
  }

  return (
    <Modal
      animationType="none"
      transparent
      visible={visible || currentSnapPoint.current !== SHEET_CLOSED}
      onRequestClose={() => {
        Animated.spring(translateY, {
          toValue: SHEET_CLOSED,
          useNativeDriver: true,
        }).start(() => {
          currentSnapPoint.current = SHEET_CLOSED;
          onClose();
        });
      }}
    >
      {/* Backdrop */}
      <Animated.View
        style={[
          styles.backdrop,
          {
            opacity: translateY.interpolate({
              inputRange: [SHEET_FULL, SHEET_HALF, SHEET_CLOSED],
              outputRange: [0.5, 0.3, 0],
            }),
          },
        ]}
        pointerEvents={visible ? 'auto' : 'none'}
      >
        <TouchableOpacity
          style={StyleSheet.absoluteFillObject}
          activeOpacity={1}
          onPress={() => {
            Animated.spring(translateY, {
              toValue: SHEET_CLOSED,
              useNativeDriver: true,
            }).start(() => {
              currentSnapPoint.current = SHEET_CLOSED;
              onClose();
            });
          }}
        />
      </Animated.View>
      
      {/* Bottom Sheet */}
      <Animated.View
        style={[
          styles.bottomSheet,
          {
            transform: [{ translateY }],
            height: SHEET_HEIGHT,
          },
        ]}
      >
          {/* Draggable Header Area */}
          <View style={styles.draggableArea}>
            {/* Drag Handle above the header */}
            <View style={styles.dragHandleContainer} {...panResponder.panHandlers}>
              <View style={styles.dragHandle} />
            </View>
            
            {/* Header with title (drag) and X (tap) */}
            <View style={styles.header}>
              <View style={styles.headerDragRegion} {...panResponder.panHandlers}>
                <View style={styles.headerLeft} />
                <Text style={styles.headerFilename} numberOfLines={1} adjustsFontSizeToFit minimumFontScale={0.7}>
                  {currentFile.filename.split('/').pop() || currentFile.filename}
                </Text>
              </View>
              <TouchableOpacity
                style={styles.closeButton}
                hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}
                onPress={() => {
                  Animated.spring(translateY, {
                    toValue: SHEET_CLOSED,
                    useNativeDriver: true,
                  }).start(() => {
                    currentSnapPoint.current = SHEET_CLOSED;
                    onClose();
                  });
                }}
              >
                <Text style={styles.closeButtonText}>✕</Text>
              </TouchableOpacity>
            </View>
          </View>
          
          <View style={styles.diffContainer}>
            <ScrollView
              style={styles.diffScrollView}
              showsVerticalScrollIndicator={true}
              bounces={false}
              {...pinchResponder.panHandlers}
            >
              <View style={styles.diffContent}>
                {diffHunks.map((hunk, index) => renderHunk(hunk, index))}
              </View>
            </ScrollView>
          </View>
          
          {/* Footer with safe bottom padding (no extra gap below) */}
          <View style={[styles.bottomBar, { paddingBottom: insets.bottom }] }>
              <TouchableOpacity
                onPress={() => navigateFile('prev')}
                disabled={currentFileIndex === 0}
                style={[styles.footerNavButton, currentFileIndex === 0 && styles.footerNavButtonDisabled]}
                activeOpacity={0.7}
              >
                <Text style={[styles.footerNavButtonText, currentFileIndex === 0 && styles.footerNavButtonTextDisabled]}>←</Text>
              </TouchableOpacity>
              
              <TouchableOpacity
                style={styles.fileDrawerButton}
                onPress={() => setShowFileDrawer(true)}
                activeOpacity={0.7}
              >
                <Text style={styles.fileDrawerButtonText}>Files ({currentFileIndex + 1}/{diffSummary.files.length})</Text>
              </TouchableOpacity>
              
              <TouchableOpacity
                onPress={() => navigateFile('next')}
                disabled={currentFileIndex === diffSummary.files.length - 1}
                style={[styles.footerNavButton, currentFileIndex === diffSummary.files.length - 1 && styles.footerNavButtonDisabled]}
                activeOpacity={0.7}
              >
                <Text style={[styles.footerNavButtonText, currentFileIndex === diffSummary.files.length - 1 && styles.footerNavButtonTextDisabled]}>→</Text>
              </TouchableOpacity>
            </View>
          
        {renderFileDrawer()}
      </Animated.View>
    </Modal>
  );
};

const styles = StyleSheet.create({
  backdrop: {
    ...StyleSheet.absoluteFillObject,
    backgroundColor: 'black',
  },
  bottomSheet: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    height: SCREEN_HEIGHT,
    backgroundColor: theme.colors.backgroundDark,
    borderTopLeftRadius: theme.borderRadius.xl,
    borderTopRightRadius: theme.borderRadius.xl,
    shadowColor: '#000',
    shadowOffset: {
      width: 0,
      height: -3,
    },
    shadowOpacity: 0.27,
    shadowRadius: 4.65,
    elevation: 6,
  },
  
  dragHandleContainer: {
    alignItems: 'center',
    paddingTop: theme.spacing.xs,
    paddingBottom: 0,
    backgroundColor: 'transparent',
  },
  dragHandle: {
    width: 40,
    height: 5,
    backgroundColor: theme.colors.textMuted,
    borderRadius: 3,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    backgroundColor: 'transparent',
    paddingHorizontal: theme.spacing.md,
    paddingTop: 0,
    paddingBottom: theme.spacing.sm,
    borderBottomWidth: 0,
    borderBottomColor: 'transparent',
  },
  headerDragRegion: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  closeButton: {
    padding: theme.spacing.sm,
    marginRight: theme.spacing.xs,
  },
  closeButtonText: {
    fontSize: 20,
    color: theme.colors.white,
    fontWeight: '300',
  },
  headerLeft: {
    width: 40,
  },
  headerFilename: {
    flex: 1,
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.white,
    textAlign: 'center',
    paddingHorizontal: theme.spacing.sm,
  },
  headerNav: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.sm,
  },
  headerProgress: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    paddingHorizontal: theme.spacing.sm,
  },
  diffContainer: {
    flex: 1,
  },
  diffScrollView: {
    flex: 1,
  },
  diffContent: {
    paddingBottom: theme.spacing.md,
  },
  diffLineContainer: {
    flexDirection: 'row',
    minHeight: 24,
  },
  additionLineBackground: {
    backgroundColor: 'rgba(34, 197, 94, 0.15)',
  },
  deletionLineBackground: {
    backgroundColor: 'rgba(239, 68, 68, 0.15)',
  },
  gutter: {
    flexDirection: 'row',
    paddingRight: theme.spacing.xs,
    paddingLeft: theme.spacing.xs,
    minWidth: 90,
    backgroundColor: 'rgba(0, 0, 0, 0.2)',
    borderRightWidth: 1,
    borderRightColor: 'rgba(255, 255, 255, 0.05)',
  },
  lineNumber: {
    fontSize: 11,
    fontFamily: theme.fontFamily.mono,
    color: 'rgba(255, 255, 255, 0.4)',
    width: 32,
    textAlign: 'right',
    paddingRight: 4,
  },
  additionLineNumber: {
    color: theme.colors.successLight,
  },
  deletionLineNumber: {
    color: theme.colors.errorLight,
  },
  changeIndicator: {
    fontSize: 12,
    fontFamily: theme.fontFamily.mono,
    width: 14,
    textAlign: 'center',
    fontWeight: 'bold',
  },
  additionIndicator: {
    color: theme.colors.successLight,
  },
  deletionIndicator: {
    color: theme.colors.errorLight,
  },
  codeContent: {
    flex: 1,
    paddingLeft: theme.spacing.sm,
  },
  diffText: {
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.white,
    paddingRight: theme.spacing.lg,
    paddingVertical: 2,
    fontSize: 12,
  },
  additionText: {
    color: theme.colors.white,
  },
  deletionText: {
    color: theme.colors.white,
  },
  headerText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.primaryLight,
    marginVertical: theme.spacing.sm,
    marginHorizontal: theme.spacing.md,
  },
  collapsedSection: {
    backgroundColor: 'rgba(255, 255, 255, 0.03)',
    borderRadius: theme.borderRadius.sm,
    marginVertical: theme.spacing.xs,
    marginHorizontal: theme.spacing.md,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.05)',
  },
  collapsedContent: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
  },
  collapsedIcon: {
    fontSize: 12,
    color: theme.colors.textMuted,
    marginRight: theme.spacing.sm,
  },
  collapsedText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.textMuted,
  },
  bottomBar: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
    backgroundColor: theme.colors.glass.dark,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  fileDrawerButton: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: 6,
    backgroundColor: theme.colors.glass.primary,
    borderRadius: theme.borderRadius.full,
  },
  fileDrawerButtonText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.white,
  },
  navButton: {
    padding: theme.spacing.xs,
  },
  navButtonDisabled: {
    opacity: 0.5,
  },
  navButtonText: {
    fontSize: 24,
    color: theme.colors.primaryLight,
    fontFamily: theme.fontFamily.regular,
    lineHeight: 24,
  },
  drawerOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    justifyContent: 'flex-end',
  },
  fileDrawer: {
    backgroundColor: theme.colors.backgroundDark,
    borderTopLeftRadius: theme.borderRadius.xl,
    borderTopRightRadius: theme.borderRadius.xl,
    maxHeight: SCREEN_HEIGHT * 0.6,
    paddingBottom: 20,
  },
  drawerHandle: {
    width: 40,
    height: 4,
    backgroundColor: theme.colors.textMuted,
    borderRadius: 2,
    alignSelf: 'center',
    marginTop: theme.spacing.sm,
    marginBottom: theme.spacing.md,
  },
  drawerTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.bold,
    color: theme.colors.white,
    paddingHorizontal: theme.spacing.md,
    marginBottom: theme.spacing.md,
  },
  fileDrawerItem: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.md,
    borderBottomWidth: 1,
    borderBottomColor: theme.colors.glass.white,
  },
  fileDrawerItemActive: {
    backgroundColor: theme.colors.glass.primary,
  },
  fileDrawerItemText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  fileDrawerItemTextActive: {
    color: theme.colors.primaryLight,
  },
  fileDrawerStats: {
    flexDirection: 'row',
    gap: theme.spacing.sm,
  },
  fileDrawerAdditions: {
    fontSize: theme.fontSize.xs,
    color: theme.colors.successLight,
  },
  fileDrawerDeletions: {
    fontSize: theme.fontSize.xs,
    color: theme.colors.errorLight,
  },
  footerNavButton: {
    padding: theme.spacing.sm,
    minWidth: 44,
    alignItems: 'center',
    justifyContent: 'center',
  },
  footerNavButtonDisabled: {
    opacity: 0.3,
  },
  footerNavButtonText: {
    fontSize: 24,
    color: theme.colors.primaryLight,
    fontFamily: theme.fontFamily.regular,
  },
  footerNavButtonTextDisabled: {
    color: theme.colors.textMuted,
  },
  syntaxContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  hunkScrollView: {
    marginVertical: theme.spacing.xs,
  },
  hunkContent: {
    minWidth: '100%',
  },
  draggableArea: {
    // Ensures the draggable area is properly bounded
    backgroundColor: 'transparent',
  },
});
