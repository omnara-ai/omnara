import React from 'react';
import { 
  View, 
  Text, 
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { Plus, Copy, Trash2 } from 'lucide-react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { Gradient, Card } from '@/components/ui';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { dashboardApi } from '@/services/api';
import { APIKey } from '@/types';
import * as Clipboard from 'expo-clipboard';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';

export const APIKeysScreen: React.FC = () => {
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  
  const { data: apiKeys, isLoading } = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => dashboardApi.getAPIKeys(),
  });

  const createKeyMutation = useMutation({
    mutationFn: (name: string) => dashboardApi.createAPIKey(name),
    onSuccess: (newKey) => {
      queryClient.invalidateQueries({ queryKey: ['api-keys'] });
      // Show the new key to the user
      Alert.alert(
        'API Key Created',
        `Your new API key has been created. Please copy it now as it won't be shown again:\n\n${newKey.api_key}`,
        [
          { 
            text: 'Copy', 
            onPress: () => {
              Clipboard.setStringAsync(newKey.api_key);
              Alert.alert('Success', 'API key copied to clipboard');
            }
          },
          { text: 'OK' }
        ]
      );
    },
  });

  const revokeKeyMutation = useMutation({
    mutationFn: (keyId: string) => dashboardApi.revokeAPIKey(keyId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['api-keys'] });
      Alert.alert('Success', 'API key revoked successfully');
    },
  });

  const handleCreateKey = () => {
    Alert.prompt(
      'Create API Key',
      'Enter a name for this API key',
      [
        { text: 'Cancel', style: 'cancel' },
        { 
          text: 'Create', 
          onPress: (name) => {
            if (name && name.trim()) {
              createKeyMutation.mutate(name.trim());
            }
          }
        }
      ],
      'plain-text',
      '',
      'default'
    );
  };

  const handleRevokeKey = (key: APIKey) => {
    Alert.alert(
      'Revoke API Key',
      `Are you sure you want to revoke "${key.name}"? This action cannot be undone.`,
      [
        { text: 'Cancel', style: 'cancel' },
        { 
          text: 'Revoke', 
          style: 'destructive',
          onPress: () => revokeKeyMutation.mutate(key.id)
        }
      ]
    );
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
            <TouchableOpacity 
              style={styles.backButton}
              onPress={() => navigation.goBack()}
              activeOpacity={0.7}
            >
              <Text style={styles.backArrow}>←</Text>
            </TouchableOpacity>
            <Text style={styles.title}>API Keys</Text>
            <Text style={styles.description}>
              Manage API keys for accessing the MCP server
            </Text>
            <TouchableOpacity 
              style={styles.createButton}
              onPress={handleCreateKey}
              activeOpacity={0.8}
            >
              <Plus size={20} color={theme.colors.white} strokeWidth={2.5} />
              <Text style={styles.createButtonText}>Create New Key</Text>
            </TouchableOpacity>
          </View>

          {apiKeys && apiKeys.length > 0 ? (
            apiKeys.map((key) => (
              <View key={key.id} style={styles.keyCardContainer}>
                <LinearGradient
                  colors={[withAlpha(theme.colors.primary, 0.12), withAlpha(theme.colors.primary, 0.06)]}
                  start={{ x: 0, y: 0 }}
                  end={{ x: 0, y: 1 }}
                  style={styles.keyCard}
                >
                  <View style={styles.keyHeader}>
                    <Text style={styles.keyName}>{key.name}</Text>
                    {key.is_active ? (
                      <View style={styles.activeBadge}>
                        <Text style={styles.activeBadgeText}>Active</Text>
                      </View>
                    ) : (
                      <View style={styles.inactiveBadge}>
                        <Text style={styles.inactiveBadgeText}>Revoked</Text>
                      </View>
                    )}
                  </View>
                  
                  <View style={styles.keyInfo}>
                    <Text style={styles.keyLabel}>Created:</Text>
                    <Text style={styles.keyValue}>
                      {new Date(key.created_at).toLocaleDateString()}
                    </Text>
                  </View>
                  
                  {key.last_used_at && (
                    <View style={styles.keyInfo}>
                      <Text style={styles.keyLabel}>Last used:</Text>
                      <Text style={styles.keyValue}>
                        {new Date(key.last_used_at).toLocaleDateString()}
                      </Text>
                    </View>
                  )}
                  
                  {key.is_active && (
                    <TouchableOpacity 
                      style={styles.revokeButton}
                      onPress={() => handleRevokeKey(key)}
                      activeOpacity={0.8}
                    >
                      <Trash2 size={16} color={theme.colors.error} strokeWidth={2} />
                      <Text style={styles.revokeButtonText}>Revoke</Text>
                    </TouchableOpacity>
                  )}
                </LinearGradient>
              </View>
            ))
          ) : (
            <View style={styles.emptyCardContainer}>
              <LinearGradient
                colors={[withAlpha(theme.colors.primary, 0.12), withAlpha(theme.colors.primary, 0.06)]}
                start={{ x: 0, y: 0 }}
                end={{ x: 0, y: 1 }}
                style={styles.emptyCard}
              >
                <Text style={styles.emptyText}>No API keys yet</Text>
                <Text style={styles.emptySubtext}>
                  Create your first API key to start using the MCP server
                </Text>
              </LinearGradient>
            </View>
          )}
        </ScrollView>
      </SafeAreaView>
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
    paddingHorizontal: theme.spacing.lg,
    paddingBottom: theme.spacing.xl * 3,
  },
  header: {
    paddingHorizontal: theme.spacing.lg,
    paddingTop: theme.spacing.lg,
    paddingBottom: theme.spacing.xl,
    marginHorizontal: -theme.spacing.lg,
  },
  backButton: {
    marginBottom: theme.spacing.md,
  },
  backArrow: {
    fontSize: theme.fontSize['2xl'],
    color: theme.colors.primaryLight,
  },
  title: {
    fontSize: theme.fontSize['3xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.md,
  },
  description: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    marginBottom: theme.spacing.md,
  },
  createButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: theme.colors.primary,
    paddingVertical: theme.spacing.md,
    paddingHorizontal: theme.spacing.lg,
    borderRadius: theme.borderRadius.lg,
  },
  createButtonText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginLeft: theme.spacing.sm,
  },
  keyCardContainer: {
    marginBottom: theme.spacing.md,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    ...theme.shadow.lg,
    shadowColor: theme.colors.primary,
    shadowOpacity: 0.15,
    overflow: 'hidden',
  },
  keyCard: {
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
  },
  keyHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: theme.spacing.md,
  },
  keyName: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  activeBadge: {
    backgroundColor: 'rgba(34, 197, 94, 0.2)',
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs / 2,
    borderRadius: theme.borderRadius.full,
    borderWidth: 1,
    borderColor: 'rgba(34, 197, 94, 0.3)',
  },
  activeBadgeText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: '#BBF7D0',
  },
  inactiveBadge: {
    backgroundColor: 'rgba(239, 68, 68, 0.2)',
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs / 2,
    borderRadius: theme.borderRadius.full,
    borderWidth: 1,
    borderColor: 'rgba(239, 68, 68, 0.3)',
  },
  inactiveBadgeText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: '#FEE2E2',
  },
  keyInfo: {
    flexDirection: 'row',
    marginBottom: theme.spacing.sm,
  },
  keyLabel: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
    marginRight: theme.spacing.sm,
  },
  keyValue: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
  },
  revokeButton: {
    flexDirection: 'row',
    alignItems: 'center',
    alignSelf: 'flex-start',
    marginTop: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
    paddingHorizontal: theme.spacing.sm,
    backgroundColor: 'rgba(239, 68, 68, 0.1)',
    borderRadius: theme.borderRadius.sm,
    borderWidth: 1,
    borderColor: 'rgba(239, 68, 68, 0.2)',
  },
  revokeButtonText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.error,
    marginLeft: theme.spacing.xs,
  },
  emptyCardContainer: {
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    ...theme.shadow.lg,
    shadowColor: theme.colors.primary,
    shadowOpacity: 0.15,
    overflow: 'hidden',
  },
  emptyCard: {
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
    alignItems: 'center',
    paddingVertical: theme.spacing.xl * 2,
  },
  emptyText: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  emptySubtext: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
    textAlign: 'center',
  },
});
