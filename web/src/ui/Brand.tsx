import type { CSSProperties } from 'react'
import { cn } from '../lib/cn'

type MarkProps = {
  size?: number
  className?: string
  title?: string
}

export function BrandMark({ size = 20, className, title }: MarkProps) {
  const h = size
  const w = (size * 80) / 100
  return (
    <svg
      className={cn('block flex-none', className)}
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 80 100"
      width={w}
      height={h}
      fill="currentColor"
      role={title ? 'img' : 'presentation'}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      <rect x="62" y="8" width="12" height="12" rx="2" />
      <path fillRule="evenodd" d="M44 30 L74 60 L44 90 L14 60 Z M38 52 h8 v8 h-8 Z" />
      <path d="M14 60 L2 48 L6 60 L2 72 Z" />
    </svg>
  )
}

type LockupProps = {
  markSize?: number
  beta?: boolean
  badge?: string
  invert?: boolean
  word?: string | null
  className?: string
  wordClassName?: string
  style?: CSSProperties
}

export function BrandLockup({
  markSize = 20,
  beta,
  badge,
  invert,
  word,
  className,
  wordClassName,
  style,
}: LockupProps) {
  const tag = badge ?? (beta ? 'BETA' : undefined)
  // 站点名尚未加载（或后台留空）时只显示图形标记，避免先闪出任何旧品牌名。
  const label = (word || '').trim()
  return (
    <span className={cn('inline-flex items-center gap-[9px] leading-none text-inherit', className)} style={style}>
      <BrandMark size={markSize} />
      {label ? (
        <span
          className={cn(
            'max-w-[12em] overflow-hidden text-ellipsis whitespace-nowrap font-sans text-[14.5px] font-extrabold tracking-[-0.02em] leading-none',
            wordClassName,
          )}
          title={label}
        >
          {label}
        </span>
      ) : null}
      {tag ? (
        <span
          className={cn(
            'border border-border px-[5px] py-px font-mono text-2xs tracking-[0.08em] leading-[1.4] text-muted',
            invert && 'border-current text-current opacity-60',
          )}
        >
          {tag}
        </span>
      ) : null}
    </span>
  )
}
