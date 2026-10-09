import { CodeXml, Moon, Sun } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { routeHref } from '@/lib/route'
import { cn } from '@/lib/utils'
import { VIEWS, type ViewName } from '@/features/shell/views'

export const REPO_URL = 'https://github.com/OmarIbannez/puente'

export function Logo() {
  return (
    <a href="#/overview" className="flex items-center gap-2.5 rounded-md" aria-label="Puente home">
      <svg viewBox="0 0 32 32" className="size-8 shrink-0" aria-hidden>
        <rect width="32" height="32" rx="8" className="fill-primary" />
        <path d="M5 21c4-8 18-8 22 0" className="stroke-primary-foreground" strokeWidth="2.6" strokeLinecap="round" fill="none" />
        <path d="M4 21h24" className="stroke-primary-foreground" strokeWidth="2.2" strokeLinecap="round" />
        <path
          d="M11 17.4V21M16 16v5M21 17.4V21"
          className="stroke-primary-foreground"
          strokeWidth="1.8"
          strokeLinecap="round"
          opacity="0.7"
        />
        <circle cx="16" cy="9" r="2" className="fill-accent" />
      </svg>
      <span className="leading-tight">
        <span className="block text-[15px] font-semibold tracking-tight">Puente</span>
        <span className="block text-[11px] text-muted-foreground">USDC · MXNB → SPEI</span>
      </span>
    </a>
  )
}

export function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))

  function toggle() {
    const next = !dark
    document.documentElement.classList.toggle('dark', next)
    localStorage.setItem('puente:theme', next ? 'dark' : 'light')
    setDark(next)
  }

  const label = dark ? 'Switch to light theme' : 'Switch to dark theme'
  return (
    <Button variant="ghost" size="icon-sm" onClick={toggle} aria-label={label} title={label}>
      {dark ? <Sun /> : <Moon />}
    </Button>
  )
}

/** The left rail on large screens; a scrolling tab strip on small ones. */
export function SideNav({ current }: { current: ViewName }) {
  return (
    <aside className="border-b border-border bg-surface-1 lg:sticky lg:top-0 lg:flex lg:h-dvh lg:flex-col lg:border-r lg:border-b-0">
      <div className="flex items-center justify-between gap-3 px-4 py-3 lg:px-5 lg:py-5">
        <Logo />
        <div className="flex items-center lg:hidden">
          <ThemeToggle />
        </div>
      </div>
      <nav aria-label="Pages" className="overflow-x-auto px-2 pb-2 lg:flex-1 lg:overflow-visible lg:px-3">
        <ul className="flex gap-1 lg:flex-col">
          {VIEWS.map((v) => {
            const active = v.name === current
            return (
              <li key={v.name}>
                <a
                  href={routeHref(v.route)}
                  aria-current={active ? 'page' : undefined}
                  className={cn(
                    'group flex items-center gap-2.5 whitespace-nowrap rounded-md px-3 py-2 text-sm transition-colors',
                    active
                      ? 'bg-card font-medium text-foreground shadow-card ring-1 ring-border'
                      : 'text-muted-foreground hover:bg-surface-2 hover:text-foreground',
                  )}
                >
                  <v.icon className={cn('size-4 shrink-0', active ? 'text-primary' : 'text-muted-foreground')} aria-hidden />
                  <span className="lg:flex lg:flex-col">
                    <span>{v.label}</span>
                    <span className="hidden text-[11px] font-normal text-muted-foreground lg:block">{v.blurb}</span>
                  </span>
                </a>
              </li>
            )
          })}
        </ul>
      </nav>
      <div className="hidden items-center justify-between border-t border-border px-5 py-3 lg:flex">
        <a
          href={REPO_URL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
        >
          <CodeXml className="size-3.5" aria-hidden />
          Source on GitHub
        </a>
        <ThemeToggle />
      </div>
    </aside>
  )
}
