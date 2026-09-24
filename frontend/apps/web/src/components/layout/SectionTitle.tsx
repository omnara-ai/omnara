import { BookOpen } from '@/components/icons'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { docsUrl, type Guide } from '@/lib/docs'

export function SectionTitle({ title, guide }: { title: string; guide?: Guide }) {
  return (
    <h2 className="type-title inline-flex items-baseline gap-1.5">
      {title}
      {guide && (
        <Tooltip>
          <TooltipTrigger asChild>
            <a
              href={docsUrl(guide)}
              target="_blank"
              rel="noreferrer"
              aria-label={`${title} guide`}
              className="text-muted-foreground hover:text-foreground self-center transition-colors"
            >
              <BookOpen className="size-4" aria-hidden="true" />
            </a>
          </TooltipTrigger>
          <TooltipContent side="right">Read the guide</TooltipContent>
        </Tooltip>
      )}
    </h2>
  )
}
