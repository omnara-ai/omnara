import { cn } from '@/lib/utils'

export function PillTabs<T extends string>({
  value,
  onValueChange,
  tabs,
}: {
  value: T
  onValueChange: (value: T) => void
  tabs: { value: T; label: string }[]
}) {
  return (
    <div className="bg-muted inline-flex max-w-full overflow-x-auto rounded-lg p-1">
      {tabs.map((tab) => (
        <button
          key={tab.value}
          type="button"
          className={cn(
            'h-10 shrink-0 whitespace-nowrap rounded-md px-3 text-sm font-medium transition-colors sm:h-auto sm:py-1',
            value === tab.value
              ? 'bg-background text-foreground'
              : 'text-muted-foreground hover:text-foreground',
          )}
          onClick={() => {
            onValueChange(tab.value)
          }}
        >
          {tab.label}
        </button>
      ))}
    </div>
  )
}
