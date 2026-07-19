import type { ReactNode } from 'react'

export default function Card({
  title,
  subtitle,
  children,
  className = '',
}: {
  title?: string
  subtitle?: string
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`card glass ${className}`}>
      {title && (
        <div className="card-head">
          <h3>{title}</h3>
          {subtitle && <span className="card-sub">{subtitle}</span>}
        </div>
      )}
      <div className="card-body">{children}</div>
    </section>
  )
}
