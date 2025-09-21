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
import { useMutation, useQuery } from '@tanstack/react-query';
import { theme } from '@/constants/theme';
import { dashboardApi } from '@/services/api';
import { reportError } from '@/lib/logger';
import { BaseModal } from '../ui/BaseModal';
import { Button } from '../ui/Button';
import { Dropdown } from '../ui/Dropdown';

interface AgentConfigModalProps {
  visible: boolean;
  onClose: () => void;
  agent?: any | null;
  onSuccess?: () => void;
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
  validation_regex?: string;
  is_secret?: boolean;
}

interface WebhookTypeSchema {
  id: string;
  name: string;
  description: string;
  icon?: string;
  build_fields: WebhookField[];
}

export const AgentConfigModal: React.FC<AgentConfigModalProps> = ({
  visible,
  onClose,
  agent,
  onSuccess,
}) => {
  const [formData, setFormData] = useState({
    name: '',
    webhook_type: null as string | null,
    webhook_config: {} as Record<string, any>,
    is_active: true,
  });

  // Fetch webhook types
  const { data: webhookTypes, isLoading: isLoadingTypes } = useQuery({
    queryKey: ['webhook-types'],
    queryFn: () => dashboardApi.getWebhookTypes(),
    enabled: visible,
  });

  // Get the selected webhook schema
  const selectedWebhookSchema = webhookTypes?.find(
    (type: WebhookTypeSchema) => type.id === formData.webhook_type
  );

  useEffect(() => {
    if (agent) {
      setFormData({
        name: agent.name,
        webhook_type: agent.webhook_type || null,
        webhook_config: agent.webhook_config || {},
        is_active: agent.is_active,
      });
    } else {
      // Reset form for new agent
      setFormData({
        name: '',
        webhook_type: null,
        webhook_config: {},
        is_active: true,
      });
    }
  }, [agent]);

  const createMutation = useMutation({
    mutationFn: (data: typeof formData) => {
      console.log('Creating user agent with data:', data);
      return dashboardApi.createUserAgent(data);
    },
    onSuccess: () => {
      Alert.alert('Success', 'Agent configuration created successfully');
      onSuccess?.();
    },
    onError: (error: any) => {
      reportError(error, {
        context: 'Create user agent error',
        tags: { feature: 'mobile-agent-config' },
      });
      Alert.alert('Error', error.message || 'Failed to create agent configuration');
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: string; data: typeof formData }) =>
      dashboardApi.updateUserAgent(id, data),
    onSuccess: () => {
      Alert.alert('Success', 'Agent configuration updated successfully');
      onSuccess?.();
    },
    onError: (error: any) => {
      Alert.alert('Error', error.message || 'Failed to update agent configuration');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => dashboardApi.deleteUserAgent(id),
    onSuccess: () => {
      Alert.alert('Success', 'Agent configuration deleted successfully');
      onSuccess?.();
    },
    onError: (error: any) => {
      Alert.alert('Error', error.message || 'Failed to delete agent configuration');
    },
  });

  const handleSubmit = () => {
    // Prepare the data
    let submitData: any = {
      name: formData.name.trim(),
      is_active: formData.is_active,
    };

    // Only include webhook fields if a webhook type is selected
    if (formData.webhook_type) {
      submitData.webhook_type = formData.webhook_type;
      submitData.webhook_config = formData.webhook_config;
    } else {
      // Explicitly set to null for "No Webhook"
      submitData.webhook_type = null;
      submitData.webhook_config = null;
    }

    if (agent) {
      updateMutation.mutate({ id: agent.id, data: submitData });
    } else {
      createMutation.mutate(submitData);
    }
  };

  const handleDelete = () => {
    if (!agent) return;
    
    Alert.alert(
      'Delete Agent',
      `Are you sure you want to delete "${agent.name}"? This action cannot be undone.`,
      [
        { text: 'Cancel', style: 'cancel' },
        { 
          text: 'Delete', 
          style: 'destructive',
          onPress: () => deleteMutation.mutate(agent.id)
        },
      ]
    );
  };

  const validateField = (field: WebhookField, value: any): string | null => {
    // Check required fields
    if (field.required && (!value || value === '')) {
      return `${field.label} is required`;
    }
    
    // Check regex validation
    if (field.validation_regex && value) {
      try {
        const regex = new RegExp(field.validation_regex);
        if (!regex.test(value)) {
          return `Invalid format for ${field.label}`;
        }
      } catch {
        // Invalid regex, skip validation
      }
    }
    
    // Check if URL is valid
    if (field.type === 'url' && value) {
      try {
        new URL(value);
      } catch {
        return 'Invalid URL format';
      }
    }
    
    return null;
  };

  const renderWebhookField = (field: WebhookField) => {
    const value = formData.webhook_config[field.name] || field.default || '';
    const error = validateField(field, value);

    switch (field.type) {
      case 'select':
        return (
          <Dropdown
            key={field.name}
            label={field.label}
            required={field.required}
            value={value}
            onValueChange={(itemValue) =>
              setFormData({
                ...formData,
                webhook_config: {
                  ...formData.webhook_config,
                  [field.name]: itemValue,
                },
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
                setFormData({
                  ...formData,
                  webhook_config: {
                    ...formData.webhook_config,
                    [field.name]: text,
                  },
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

      case 'password':
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
                setFormData({
                  ...formData,
                  webhook_config: {
                    ...formData.webhook_config,
                    [field.name]: text,
                  },
                })
              }
              secureTextEntry
              autoCapitalize="none"
              autoCorrect={false}
            />
            {field.description && (
              <Text style={styles.helperText}>{field.description}</Text>
            )}
          </View>
        );

      case 'url':
        return (
          <View key={field.name} style={styles.inputGroup}>
            <Text style={styles.label}>
              {field.label}
              {field.required && <Text style={styles.required}> *</Text>}
            </Text>
            <TextInput
              style={[
                styles.input,
                error && value ? styles.inputError : null,
              ]}
              placeholder={field.placeholder}
              placeholderTextColor={theme.colors.textMuted}
              value={value}
              onChangeText={(text) =>
                setFormData({
                  ...formData,
                  webhook_config: {
                    ...formData.webhook_config,
                    [field.name]: text,
                  },
                })
              }
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
            />
            {error && value ? (
              <Text style={styles.errorText}>{error}</Text>
            ) : field.description ? (
              <Text style={styles.helperText}>{field.description}</Text>
            ) : null}
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
                setFormData({
                  ...formData,
                  webhook_config: {
                    ...formData.webhook_config,
                    [field.name]: text,
                  },
                })
              }
              autoCapitalize="none"
              autoCorrect={false}
            />
            {field.description && (
              <Text style={styles.helperText}>{field.description}</Text>
            )}
          </View>
        );
    }
  };

  const isLoading = createMutation.isPending || updateMutation.isPending;
  const isDeleting = deleteMutation.isPending;

  // Check if all required fields are filled - matches frontend logic
  const isFormValid = () => {
    // Name is always required
    if (!formData.name.trim()) return false;
    
    // If a webhook type is selected, validate its required fields
    if (formData.webhook_type && selectedWebhookSchema) {
      for (const field of selectedWebhookSchema.build_fields) {
        const value = formData.webhook_config[field.name];
        
        // Check required fields
        if (field.required) {
          // Check for empty values
          if (value === undefined || value === null || value === '') {
            return false;
          }
          // For strings, check if not just whitespace
          if (typeof value === 'string' && !value.trim()) {
            return false;
          }
        }
        
        // Validate URLs (even if optional, if provided must be valid)
        if (field.type === 'url' && value && typeof value === 'string' && value.trim()) {
          try {
            new URL(value);
          } catch {
            return false; // Invalid URL format
          }
        }
        
        // Validate against regex pattern if provided
        if (field.validation_regex && value && typeof value === 'string') {
          try {
            const regex = new RegExp(field.validation_regex);
            if (!regex.test(value)) {
              return false;
            }
          } catch {
            // Invalid regex, skip validation
          }
        }
      }
    }
    
    // Form is valid if name is present and webhook fields (if any) are valid
    return true;
  };

  return (
    <BaseModal
      visible={visible}
      onClose={onClose}
      title={agent ? 'Edit Agent Configuration' : 'Create New Agent'}
      onSubmit={handleSubmit}
      submitText={agent ? 'Update' : 'Create'}
      isLoading={isLoading}
      isDisabled={!isFormValid()}
    >
      <ScrollView style={styles.scrollView} showsVerticalScrollIndicator={false}>
        <View style={styles.inputGroup}>
          <Text style={styles.label}>
            Agent Name <Text style={styles.required}>*</Text>
          </Text>
          <TextInput
            style={styles.input}
            placeholder="My Custom Agent"
            placeholderTextColor={theme.colors.textMuted}
            value={formData.name}
            onChangeText={(text) => setFormData({ ...formData, name: text })}
            autoCapitalize="words"
            autoCorrect={false}
          />
        </View>

        {isLoadingTypes ? (
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="small" color={theme.colors.primary} />
            <Text style={styles.loadingText}>Loading webhook types...</Text>
          </View>
        ) : (
          <Dropdown
            label="Webhook Type"
            value={formData.webhook_type}
            onValueChange={(value) => {
              setFormData({
                ...formData,
                webhook_type: value,
                webhook_config: value ? {} : null, // Reset config, null if no webhook
              });
            }}
            options={[
              { label: 'No Webhook', value: null },
              ...(webhookTypes?.map((type: WebhookTypeSchema) => ({
                label: type.name,
                value: type.id,
              })) || []),
            ]}
            placeholder="Select webhook type"
            helperText={selectedWebhookSchema?.description}
          />
        )}

        {/* Render dynamic webhook fields based on selected type */}
        {selectedWebhookSchema?.build_fields.map(renderWebhookField)}

        {agent && (
          <View style={styles.deleteSection}>
            <Button
              onPress={handleDelete}
              variant="glass"
              style={styles.deleteButton}
              loading={isDeleting}
              disabled={isLoading || isDeleting}
            >
              <Text style={styles.deleteButtonText}>Delete Agent</Text>
            </Button>
          </View>
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
    padding: theme.spacing.lg,
  },
  loadingText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    marginTop: theme.spacing.sm,
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
  helperText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    marginTop: theme.spacing.xs,
  },
  inputError: {
    borderColor: theme.colors.error,
  },
  errorText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.error,
    marginTop: theme.spacing.xs,
  },
  deleteSection: {
    marginTop: theme.spacing.xl,
  },
  deleteButton: {
    backgroundColor: 'rgba(239, 68, 68, 0.2)',
    borderColor: 'rgba(239, 68, 68, 0.3)',
    borderWidth: 1,
  },
  deleteButtonText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: '#ef4444',
  },
});
