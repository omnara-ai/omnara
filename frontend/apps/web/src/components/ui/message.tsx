import * as React from 'react'

import { cn } from '@/lib/utils'

function MessageGroup({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="message-group"
      className={cn('flex min-w-0 flex-col gap-2', className)}
      {...props}
    />
  )
}

function Message({
  className,
  align = 'start',
  ...props
}: React.ComponentProps<'div'> & { align?: 'start' | 'end' }) {
  return (
    <div
      data-slot="message"
      data-align={align}
      className={cn(
        'group/message relative flex w-full min-w-0 gap-2 text-sm data-[align=end]:flex-row-reverse',
        className,
      )}
      {...props}
    />
  )
}

function MessageAvatar({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="message-avatar"
      className={cn(
        'bg-muted group-has-data-[slot=message-footer]/message:-translate-y-8 flex w-fit min-w-8 shrink-0 items-center justify-center self-end overflow-hidden rounded-full',
        className,
      )}
      {...props}
    />
  )
}

function MessageContent({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="message-content"
      className={cn(
        'wrap-break-word group-data-[align=end]/message:*:data-slot:self-end flex w-full min-w-0 flex-col gap-2.5',
        className,
      )}
      {...props}
    />
  )
}

function MessageHeader({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="message-header"
      className={cn(
        'text-muted-foreground group-has-data-[variant=ghost]/message:px-0 flex min-w-0 max-w-full items-center px-3 text-xs font-medium',
        className,
      )}
      {...props}
    />
  )
}

function MessageFooter({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="message-footer"
      className={cn(
        'text-muted-foreground group-has-data-[variant=ghost]/message:px-0 flex min-w-0 max-w-full items-center px-3 text-xs font-medium group-data-[align=end]/message:justify-end',
        className,
      )}
      {...props}
    />
  )
}

export { Message, MessageAvatar, MessageContent, MessageFooter, MessageGroup, MessageHeader }
