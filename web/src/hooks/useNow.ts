import { useEffect, useState } from 'react'

/** The current time in milliseconds, renewed every intervalMs, so that "2 days left" stays true on a page left open. */
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}
