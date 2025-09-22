import { useState, useEffect, useCallback } from 'react';
import { Platform } from 'react-native';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { subscriptionService } from '@/services/subscriptionService';
import { Subscription } from '@/types/subscription';

export const useSubscription = () => {
  const queryClient = useQueryClient();

  // Query for subscription status
  const {
    data: subscription,
    isLoading,
    error,
    refetch,
  } = useQuery({
    queryKey: ['subscription-status'],
    queryFn: () => subscriptionService.getSubscriptionStatus(),
    enabled: Platform.OS === 'ios',
    staleTime: 1000 * 60 * 5, // 5 minutes
  });


  // Purchase mutation
  const purchaseMutation = useMutation({
    mutationFn: () => subscriptionService.purchasePro(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['subscription-status'] });
    },
  });

  // Restore mutation
  const restoreMutation = useMutation({
    mutationFn: () => subscriptionService.restorePurchases(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['subscription-status'] });
    },
  });

  const purchase = useCallback(async () => {
    if (Platform.OS !== 'ios') {
      throw new Error('Purchases only available on iOS');
    }
    return purchaseMutation.mutateAsync();
  }, [purchaseMutation]);

  const restore = useCallback(async () => {
    if (Platform.OS !== 'ios') {
      throw new Error('Restore only available on iOS');
    }
    return restoreMutation.mutateAsync();
  }, [restoreMutation]);

  return {
    subscription: subscription || {
      isActive: false,
      productId: null,
      expirationDate: null,
      willRenew: false,
      periodType: null,
    },
    isLoading,
    error,
    refetch,
    purchase,
    restore,
    isPurchasing: purchaseMutation.isPending,
    isRestoring: restoreMutation.isPending,
    purchaseError: purchaseMutation.error,
    restoreError: restoreMutation.error,
    isProUser: subscription?.isActive || false,
  };
};