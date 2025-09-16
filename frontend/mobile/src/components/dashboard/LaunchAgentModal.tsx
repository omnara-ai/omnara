import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TextInput,
  Alert,
  ScrollView,
  ActivityIndicator,
} from 'react-native';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { theme } from '@/constants/theme';
import { dashboardApi } from '@/services/api';
import { BaseModal } from '../ui/BaseModal';
import { Dropdown } from '../ui/Dropdown';

interface LaunchAgentModalProps {
  visible: boolean;
  onClose: () => void;
  agent: any;
  onLaunchSuccess?: (instanceId: string) => void;
}

interface WebhookField {
  name: string;
  label: string;
  type: 'string' | 'text' | 'password' | 'select' | 'boolean' | 'number' | 'url';
  required: boolean;
  description?: string;
  placeholder?: string;
  default?: any;
  options?: Array<{ label: string; value: string }>;
}

interface WebhookTypeSchema {
  id: string;
  name: string;
  description: string;
  icon?: string;
  runtime_fields: WebhookField[];
}

export const LaunchAgentModal: React.FC<LaunchAgentModalProps> = ({
  visible,
  onClose,
  agent,
  onLaunchSuccess,
}) => {
  const [runtimeData, setRuntimeData] = useState<Record<string, any>>({});
  const queryClient = useQueryClient();

  // Fetch webhook types
  const { data: webhookTypes, isLoading: isLoadingTypes } = useQuery({
    queryKey: ['webhook-types'],
    queryFn: () => dashboardApi.getWebhookTypes(),
    enabled: visible && !!agent?.webhook_type,
  });

  // Get the webhook schema for this agent
  const webhookSchema = webhookTypes?.find(
    (type: WebhookTypeSchema) => type.id === agent?.webhook_type
  );

  // Reset runtime data when agent changes
  useEffect(() => {
    setRuntimeData({});
  }, [agent]);

  const createInstanceMutation = useMutation({
    mutationFn: ({ agentId, data }: { agentId: string; data: Record<string, any> }) =>
      dashboardApi.createAgentInstance(agentId, data),
    onSuccess: (data) => {
      if (data.success && data.agent_instance_id) {
        queryClient.invalidateQueries({ queryKey: ['agent-types'] });
        queryClient.invalidateQueries({ queryKey: ['user-agents'] });
        
        // Show success message
        Alert.alert('Success', 'Agent instance created successfully');
        
        // Reset form
        setRuntimeData({});
        
        // Call success callback
        onLaunchSuccess?.(data.agent_instance_id);
        
        // Close modal
        onClose();
      } else {
        // Display the actual error message from the backend
        const errorMessage = data.error || data.message || 'Failed to create agent instance';
        Alert.alert('Error', errorMessage);
      }
    },
    onError: (error: any) => {
      // Use error title if available, otherwise default to 'Error'
      const title = error.title || 'Error';
      const message = error.message || 'Failed to create agent instance';
      Alert.alert(title, message);
    },
  });

  const handleSubmit = () => {
    // Check required fields
    if (webhookSchema) {
      for (const field of webhookSchema.runtime_fields) {
        if (field.required) {
          const value = runtimeData[field.name];
          if (!value || (typeof value === 'string' && !value.trim())) {
            Alert.alert('Error', `Please provide ${field.label}`);
            return;
          }
        }
      }
    }

    createInstanceMutation.mutate({
      agentId: agent.id,
      data: runtimeData,
    });
  };

  const renderRuntimeField = (field: WebhookField) => {
    const value = runtimeData[field.name] || field.default || '';

    switch (field.type) {
      case 'select':
        return (
          <Dropdown
            key={field.name}
            label={field.label}
            required={field.required}
            value={value}
            onValueChange={(itemValue) =>
              setRuntimeData({
                ...runtimeData,
                [field.name]: itemValue,
              })
            }
            options={[
              { label: 'Select...', value: '' },
              ...(field.options?.map((option) => ({
                label: option.label,
                value: option.value,
              })) || []),
            ]}
            placeholder="Select an option"
            helperText={field.description}
          />
        );

      case 'text':
        return (
          <View key={field.name} style={styles.inputGroup}>
            <Text style={styles.label}>
              {field.label}
              {field.required && <Text style={styles.required}> *</Text>}
            </Text>
            <TextInput
              style={styles.textArea}
              placeholder={field.placeholder}
              placeholderTextColor={theme.colors.textMuted}
              value={value}
              onChangeText={(text) =>
                setRuntimeData({
                  ...runtimeData,
                  [field.name]: text,
                })
              }
              multiline
              numberOfLines={4}
              textAlignVertical="top"
            />
            {field.description && (
              <Text style={styles.helperText}>{field.description}</Text>
            )}
          </View>
        );

      default:
        return (
          <View key={field.name} style={styles.inputGroup}>
            <Text style={styles.label}>
              {field.label}
              {field.required && <Text style={styles.required}> *</Text>}
            </Text>
            <TextInput
              style={styles.input}
              placeholder={field.placeholder}
              placeholderTextColor={theme.colors.textMuted}
              value={value}
              onChangeText={(text) =>
                setRuntimeData({
                  ...runtimeData,
                  [field.name]: text,
                })
              }
              autoCapitalize={field.name === 'name' || field.name === 'branch_name' || field.name === 'worktree_name' ? 'none' : 'sentences'}
              autoCorrect={false}
            />
            {field.description && (
              <Text style={styles.helperText}>{field.description}</Text>
            )}
          </View>
        );
    }
  };

  // Check if all required fields are filled
  const isFormValid = () => {
    if (!webhookSchema) {
      // No webhook configured, can't launch
      return false;
    }

    for (const field of webhookSchema.runtime_fields) {
      if (field.required) {
        const value = runtimeData[field.name];
        if (!value || (typeof value === 'string' && !value.trim())) {
          return false;
        }
      }
    }
    
    return true;
  };

  // Show loading state while fetching webhook types
  if (agent?.webhook_type && isLoadingTypes) {
    return (
      <BaseModal
        visible={visible}
        onClose={onClose}
        title={`Start ${agent?.name}`}
        onSubmit={() => {}}
        submitText="Start Agent"
        isLoading={true}
        isDisabled={true}
      >
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={theme.colors.primary} />
          <Text style={styles.loadingText}>Loading configuration...</Text>
        </View>
      </BaseModal>
    );
  }

  // Show message if no webhook configured
  if (!agent?.webhook_type) {
    return (
      <BaseModal
        visible={visible}
        onClose={onClose}
        title={`Start ${agent?.name}`}
        onSubmit={() => {}}
        submitText="Start Agent"
        isDisabled={true}
      >
        <View style={styles.messageContainer}>
          <Text style={styles.messageText}>
            This agent needs to be configured with a webhook before it can be started.
          </Text>
          <Text style={styles.helperText}>
            Please edit the agent configuration to add a webhook.
          </Text>
        </View>
      </BaseModal>
    );
  }

  return (
    <BaseModal
      visible={visible}
      onClose={onClose}
      title={`Start ${agent?.name}`}
      onSubmit={handleSubmit}
      submitText={createInstanceMutation.isPending ? 'Creating...' : 'Start Agent'}
      isLoading={createInstanceMutation.isPending}
      isDisabled={!isFormValid()}
    >
      <ScrollView style={styles.scrollView} showsVerticalScrollIndicator={false}>
        {webhookSchema && (
          <>
            <View style={styles.webhookInfo}>
              <Text style={styles.webhookType}>{webhookSchema.name}</Text>
              <Text style={styles.webhookDescription}>{webhookSchema.description}</Text>
            </View>

            {/* Render dynamic runtime fields based on webhook type */}
            {webhookSchema.runtime_fields.length > 0 ? (
              webhookSchema.runtime_fields.map(renderRuntimeField)
            ) : (
              <View style={styles.messageContainer}>
                <Text style={styles.messageText}>
                  This webhook doesn't require any additional information.
                </Text>
              </View>
            )}
          </>
        )}
      </ScrollView>
    </BaseModal>
  );
};

const styles = StyleSheet.create({
  scrollView: {
    maxHeight: 400,
  },
  loadingContainer: {
    alignItems: 'center',
    padding: theme.spacing.xl,
  },
  loadingText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    marginTop: theme.spacing.md,
  },
  messageContainer: {
    padding: theme.spacing.lg,
    alignItems: 'center',
  },
  messageText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    textAlign: 'center',
  },
  webhookInfo: {
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    borderRadius: theme.borderRadius.md,
    padding: theme.spacing.md,
    marginBottom: theme.spacing.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
  },
  webhookType: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.primary,
    marginBottom: theme.spacing.xs,
  },
  webhookDescription: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  inputGroup: {
    marginBottom: theme.spacing.lg,
  },
  label: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  required: {
    color: theme.colors.error,
  },
  textArea: {
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderRadius: theme.borderRadius.md,
    padding: theme.spacing.md,
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    minHeight: 100,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  input: {
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderRadius: theme.borderRadius.md,
    padding: theme.spacing.md,
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    height: 48,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  helperText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    marginTop: theme.spacing.xs,
  },
});