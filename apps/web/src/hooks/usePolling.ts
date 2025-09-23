import { useEffect, useRef } from 'react'

export function usePolling(callback: () => void, interval: number = 5000) {
  const callbackRef = useRef(callback)
  
  useEffect(() => {
    callbackRef.current = callback
  }, [callback])

  useEffect(() => {
    const tick = () => callbackRef.current()
    
    tick() // Call immediately
    
    // Only set up interval if interval > 0
    if (interval > 0) {
      const id = setInterval(tick, interval)
      return () => clearInterval(id)
    }
  }, [interval])
} 