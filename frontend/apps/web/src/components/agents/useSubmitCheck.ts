import { useState } from 'react'

type SubmitBlock = 'unchanged' | 'problems' | null

export function useSubmitCheck(problems: string[], unchanged = false) {
  const current: SubmitBlock = problems.length > 0 ? 'problems' : unchanged ? 'unchanged' : null
  const [shown, setShown] = useState<SubmitBlock>(null)
  if (shown !== null && shown !== current) setShown(null)

  return {
    problems: shown === 'problems' ? problems : [],
    unchanged: shown === 'unchanged',
    check: () => {
      setShown(current)
      return current === null
    },
  }
}
