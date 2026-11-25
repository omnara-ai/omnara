import { cursorID } from './assistant-cursor'

/** The assistant's own cursor, shown while it clicks a link for the user. */
export function AssistantCursor() {
  return (
    <div
      id={cursorID}
      data-assistant-surface
      data-state="hidden"
      aria-hidden="true"
      className="group/cursor pointer-events-none fixed top-0 left-0 z-[70] transition-[transform,opacity] ease-out data-[state=hidden]:opacity-0"
    >
      <span className="bg-primary/40 absolute -top-3.5 -left-3.5 size-8 scale-0 rounded-full transition-transform duration-200 group-data-[state=pressing]/cursor:scale-100" />
      <svg
        viewBox="0 0 24 24"
        className="text-primary relative size-7 origin-top-left drop-shadow-md transition-transform duration-100 group-data-[state=pressing]/cursor:scale-90"
      >
        <path
          d="M3 2l7.5 18 2.2-7.3L20 10.5z"
          fill="currentColor"
          stroke="white"
          strokeWidth="1.5"
          strokeLinejoin="round"
        />
      </svg>
      <span className="bg-primary text-primary-foreground absolute top-6 left-5 rounded-md px-1.5 py-0.5 text-xs font-medium whitespace-nowrap shadow-sm group-data-[label=before]/cursor:right-full group-data-[label=before]/cursor:left-auto">
        Assistant
      </span>
    </div>
  )
}
