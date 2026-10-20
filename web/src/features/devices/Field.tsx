import type { ReactNode } from 'react'

/** A label and its control in the grid of a dialog form (the .form class). */
export function Field({
  label,
  error,
  help,
  children,
}: {
  label: string
  error?: string
  help?: ReactNode
  children: ReactNode
}) {
  return (
    <>
      <span className="form-label">{label}</span>
      <div className="form-field">
        {children}
        {error && <div className="field-error">{error}</div>}
        {help && <div className="small muted">{help}</div>}
      </div>
    </>
  )
}
