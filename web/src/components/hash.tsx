import { Check, Copy, ExternalLink } from 'lucide-react'
import { useState } from 'react'

import { shortHash } from '@/lib/format'
import { cn } from '@/lib/utils'

export function CopyButton({ value, label = 'Copy', className }: { value: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false)
  function copy() {
    void navigator.clipboard.writeText(value).then(() => {
      setCopied(true)
      window.setTimeout(() => {
        setCopied(false)
      }, 1500)
    })
  }
  return (
    <button
      type="button"
      onClick={copy}
      aria-label={copied ? 'Copied' : label}
      title={copied ? 'Copied' : label}
      className={cn(
        'inline-grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-muted-foreground transition-colors hover:bg-surface-2 hover:text-foreground',
        className,
      )}
    >
      {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
    </button>
  )
}

interface HashProps {
  value: string
  /** Show the whole value instead of a shortened one. */
  full?: boolean
  href?: string
  copy?: boolean
  className?: string
}

/** A hash, address or clave in mono, optionally linked to an explorer and copyable. */
export function Hash({ value, full = false, href, copy = true, className }: HashProps) {
  const text = full ? value : shortHash(value)
  return (
    <span className={cn('inline-flex max-w-full items-center gap-0.5 font-mono text-xs', className)}>
      {href ? (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          title={value}
          className="inline-flex min-w-0 items-center gap-1 text-primary underline-offset-4 hover:underline"
        >
          <span className="truncate">{text}</span>
          <ExternalLink className="size-3 shrink-0" aria-hidden />
          <span className="sr-only">(opens explorer)</span>
        </a>
      ) : (
        <span className="truncate" title={value}>
          {text}
        </span>
      )}
      {copy && <CopyButton value={value} label="Copy to clipboard" />}
    </span>
  )
}
