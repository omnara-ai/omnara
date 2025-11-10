import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'
import { toast } from './use-toast'
import {
  PromptQueueItem,
  PromptQueueStatus,
  QueueStatusResponse,
} from '@/types/dashboard'

/**
 * Hook to fetch queue items for an instance
 */
export function useQueue(instanceId: string, status?: PromptQueueStatus) {
  return useQuery({
    queryKey: ['queue', instanceId, status],
    queryFn: () => apiClient.getQueue(instanceId, status),
    enabled: !!instanceId,
  })
}

/**
 * Hook to fetch queue status/statistics
 */
export function useQueueStatus(instanceId: string) {
  return useQuery({
    queryKey: ['queue-status', instanceId],
    queryFn: () => apiClient.getQueueStatus(instanceId),
    enabled: !!instanceId,
  })
}

/**
 * Hook to add prompts to queue
 */
export function useAddPrompts(instanceId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (prompts: string[]) =>
      apiClient.addPromptsToQueue(instanceId, prompts),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['queue', instanceId] })
      queryClient.invalidateQueries({ queryKey: ['queue-status', instanceId] })
      toast({
        title: 'Prompts added',
        description: 'Your prompts have been added to the queue',
      })
    },
    onError: (error: Error) => {
      toast({
        title: 'Failed to add prompts',
        description: error.message,
        variant: 'destructive',
      })
    },
  })
}

/**
 * Hook to reorder queue items
 */
export function useReorderQueue(instanceId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (queueItemIds: string[]) =>
      apiClient.reorderQueue(instanceId, queueItemIds),
    onMutate: async (newOrder) => {
      // Cancel outgoing refetches
      await queryClient.cancelQueries({ queryKey: ['queue', instanceId] })

      // Snapshot previous value
      const previousQueue = queryClient.getQueryData<PromptQueueItem[]>([
        'queue',
        instanceId,
      ])

      // Optimistically update
      if (previousQueue) {
        const reordered = newOrder
          .map((id) => previousQueue.find((item) => item.id === id))
          .filter(Boolean) as PromptQueueItem[]

        queryClient.setQueryData(['queue', instanceId], reordered)
      }

      return { previousQueue }
    },
    onError: (error: Error, _newOrder, context) => {
      // Rollback on error
      if (context?.previousQueue) {
        queryClient.setQueryData(['queue', instanceId], context.previousQueue)
      }
      toast({
        title: 'Failed to reorder queue',
        description: error.message,
        variant: 'destructive',
      })
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['queue', instanceId] })
    },
  })
}

/**
 * Hook to update a queue item's prompt text
 */
export function useUpdateQueueItem(instanceId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ queueItemId, promptText }: { queueItemId: string; promptText: string }) =>
      apiClient.updateQueueItem(queueItemId, promptText),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['queue', instanceId] })
      toast({
        title: 'Prompt updated',
        description: 'Your prompt has been updated',
      })
    },
    onError: (error: Error) => {
      toast({
        title: 'Failed to update prompt',
        description: error.message,
        variant: 'destructive',
      })
    },
  })
}

/**
 * Hook to delete a queue item
 */
export function useDeleteQueueItem(instanceId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (queueItemId: string) => apiClient.deleteQueueItem(queueItemId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['queue', instanceId] })
      queryClient.invalidateQueries({ queryKey: ['queue-status', instanceId] })
      toast({
        title: 'Prompt removed',
        description: 'The prompt has been removed from the queue',
      })
    },
    onError: (error: Error) => {
      toast({
        title: 'Failed to remove prompt',
        description: error.message,
        variant: 'destructive',
      })
    },
  })
}

/**
 * Hook to clear all pending prompts
 */
export function useClearQueue(instanceId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: () => apiClient.clearQueue(instanceId),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['queue', instanceId] })
      queryClient.invalidateQueries({ queryKey: ['queue-status', instanceId] })
      toast({
        title: 'Queue cleared',
        description: `${data.deleted_count} prompt(s) removed from queue`,
      })
    },
    onError: (error: Error) => {
      toast({
        title: 'Failed to clear queue',
        description: error.message,
        variant: 'destructive',
      })
    },
  })
}
