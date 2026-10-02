import { type KeyboardEvent, useEffect, useRef } from 'react'

/**
 * Keyboard use of the model picker: arrows move between rows and back to search, typing on
 * a row edits the search, and ⌘/Ctrl+Enter submits. Focus stays at the same list position
 * when a toggle moves the focused row.
 */
export function useModelPickerKeyboard({
  onTypeAhead,
  onSearchEnter,
  onSubmitShortcut,
}: {
  /** A printable key or Backspace pressed on a row, to apply to the search. */
  onTypeAhead: (key: string) => void
  onSearchEnter: () => void
  onSubmitShortcut: () => void
}) {
  const searchRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  // Row to focus after a toggle moves the focused row elsewhere in the list.
  const refocusRow = useRef<number | null>(null)

  function rows() {
    return [...(listRef.current?.querySelectorAll<HTMLElement>('[data-picker-row]') ?? [])]
  }

  useEffect(() => {
    searchRef.current?.focus()
  }, [])

  useEffect(() => {
    const index = refocusRow.current
    if (index === null) return
    refocusRow.current = null
    const current = rows()
    ;(current[Math.min(index, current.length - 1)] ?? searchRef.current)?.focus()
  })

  function focusFrom(event: KeyboardEvent<HTMLElement>, next: HTMLElement | null | undefined) {
    event.preventDefault()
    next?.focus()
  }

  return {
    searchRef,
    listRef,
    /** Call before a toggle that moves the focused row. */
    holdFocusedRow: () => {
      const index = rows().findIndex((row) => row === document.activeElement)
      if (index !== -1) refocusRow.current = index
    },
    releaseHeldRow: () => {
      refocusRow.current = null
    },
    onSearchKeyDown: (event: KeyboardEvent<HTMLInputElement>) => {
      if (event.key === 'ArrowDown') {
        focusFrom(event, rows()[0])
      } else if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) {
        event.preventDefault()
        onSearchEnter()
      }
    },
    onRowKeyDown: (event: KeyboardEvent<HTMLElement>) => {
      const row = event.currentTarget
      const all = rows()
      const index = all.indexOf(row)
      const modified = event.metaKey || event.ctrlKey || event.altKey
      if (event.key === 'ArrowDown') focusFrom(event, all[index + 1])
      else if (event.key === 'ArrowUp')
        focusFrom(event, index === 0 ? searchRef.current : all[index - 1])
      else if (event.key === 'Home') focusFrom(event, all[0])
      else if (event.key === 'End') focusFrom(event, all.at(-1))
      else if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) {
        event.preventDefault()
        row.click()
      } else if (
        !modified &&
        ((event.key.length === 1 && event.key !== ' ') || event.key === 'Backspace')
      ) {
        event.preventDefault()
        onTypeAhead(event.key)
        searchRef.current?.focus()
      }
    },
    onViewKeyDown: (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.key !== 'Enter' || !(event.metaKey || event.ctrlKey)) return
      event.preventDefault()
      onSubmitShortcut()
    },
  }
}
