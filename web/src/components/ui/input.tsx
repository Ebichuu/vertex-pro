// Adapted from shadcn/ui (MIT); see web/SHADCN-LICENSE.md.
import * as React from 'react'
import { cn } from '../../lib/utils'

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(function Input({ className, type, ...props }, ref) {
  const checkable = type === 'checkbox' || type === 'radio'
  return <input ref={ref} type={type} data-slot="input" className={cn(
    checkable ? 'size-4 shrink-0 accent-primary' : 'flex h-9 w-full min-w-0 rounded-md border border-input bg-card px-3 py-1 text-sm text-foreground placeholder:text-muted-foreground',
    'transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive',
    className,
  )} {...props} />
})
