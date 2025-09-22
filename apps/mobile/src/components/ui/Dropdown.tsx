import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  LayoutAnimation,
  Platform,
  UIManager,
} from 'react-native';
import { ChevronDown, ChevronUp, Check } from 'lucide-react-native';
import { theme } from '@/constants/theme';

// Enable LayoutAnimation on Android
if (Platform.OS === 'android' && UIManager.setLayoutAnimationEnabledExperimental) {
  UIManager.setLayoutAnimationEnabledExperimental(true);
}

interface DropdownOption {
  label: string;
  value: string | null;
}

interface DropdownProps {
  value: string | null;
  onValueChange: (value: string | null) => void;
  options: DropdownOption[];
  placeholder?: string;
  label?: string;
  required?: boolean;
  helperText?: string;
}

export const Dropdown: React.FC<DropdownProps> = ({
  value,
  onValueChange,
  options,
  placeholder = 'Select...',
  label,
  required,
  helperText,
}) => {
  const [isOpen, setIsOpen] = useState(false);

  const selectedOption = options.find((opt) => opt.value === value);

  const handleSelect = (option: DropdownOption) => {
    LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
    onValueChange(option.value);
    setIsOpen(false);
  };

  const toggleDropdown = () => {
    LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
    setIsOpen(!isOpen);
  };

  return (
    <View style={styles.container}>
      {label && (
        <Text style={styles.label}>
          {label}
          {required && <Text style={styles.required}> *</Text>}
        </Text>
      )}
      
      <View>
        <TouchableOpacity
          style={[styles.selector, isOpen && styles.selectorOpen]}
          onPress={toggleDropdown}
          activeOpacity={0.7}
        >
          <Text style={[styles.selectorText, !selectedOption && styles.placeholderText]}>
            {selectedOption?.label || placeholder}
          </Text>
          {isOpen ? (
            <ChevronUp size={20} color={theme.colors.textMuted} />
          ) : (
            <ChevronDown size={20} color={theme.colors.textMuted} />
          )}
        </TouchableOpacity>

        {isOpen && (
          <View style={styles.optionsContainer}>
            {options.map((option, index) => (
              <TouchableOpacity
                key={option.value?.toString() || 'null'}
                style={[
                  styles.option,
                  index === 0 && styles.firstOption,
                  index === options.length - 1 && styles.lastOption,
                  option.value === value && styles.selectedOption,
                ]}
                onPress={() => handleSelect(option)}
                activeOpacity={0.7}
              >
                <Text
                  style={[
                    styles.optionText,
                    option.value === value && styles.selectedOptionText,
                  ]}
                >
                  {option.label}
                </Text>
                {option.value === value && (
                  <Check size={16} color={theme.colors.primary} />
                )}
              </TouchableOpacity>
            ))}
          </View>
        )}
      </View>

      {helperText && (
        <Text style={styles.helperText}>{helperText}</Text>
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
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
  selector: {
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderRadius: theme.borderRadius.md,
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    minHeight: 48,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  selectorOpen: {
    borderBottomLeftRadius: 0,
    borderBottomRightRadius: 0,
    borderBottomColor: 'rgba(255, 255, 255, 0.05)',
  },
  selectorText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    flex: 1,
  },
  placeholderText: {
    color: theme.colors.textMuted,
  },
  optionsContainer: {
    backgroundColor: 'rgba(255, 255, 255, 0.05)',
    borderBottomLeftRadius: theme.borderRadius.md,
    borderBottomRightRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderTopWidth: 0,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    overflow: 'hidden',
  },
  option: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    minHeight: 44,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.05)',
  },
  firstOption: {
    borderTopWidth: 0,
  },
  lastOption: {
    borderBottomWidth: 0,
  },
  selectedOption: {
    backgroundColor: 'rgba(255, 255, 255, 0.08)',
  },
  optionText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    flex: 1,
  },
  selectedOptionText: {
    color: theme.colors.primary,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
  },
  helperText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    marginTop: theme.spacing.xs,
  },
});