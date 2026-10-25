// Adapted from shadcn/ui (MIT); see web/SHADCN-LICENSE.md.
import * as React from 'react'
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '../../lib/utils'

const buttonVariants = cva(
  'inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0 [&.active]:bg-primary [&.active]:text-primary-foreground aria-pressed:bg-primary aria-pressed:text-primary-foreground',
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground hover:bg-primary/90',
        outline: 'border border-input bg-card text-foreground hover:bg-accent',
        secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
        ghost: 'text-foreground hover:bg-accent',
        destructive: 'bg-destructive-solid text-destructive-foreground hover:bg-destructive-solid/90',
        link: 'text-primary underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-9 px-3.5 py-2', sm: 'h-8 gap-1.5 px-2.5 text-xs', lg: 'h-10 px-5', icon: 'size-9 p-0',
      },
    },
    defaultVariants: { variant: 'outline', size: 'default' },
  },
)

type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants> & { asChild?: boolean }
export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(function Button({ className, variant, size, asChild = false, ...props }, ref) {
  const Component = asChild ? Slot : 'button'
  return <Component ref={ref} data-slot="button" className={cn(buttonVariants({ variant, size }), className, '[&.danger]:text-destructive')} {...props} />
})
export { buttonVariants }
