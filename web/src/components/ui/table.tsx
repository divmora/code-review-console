"use client"

import * as React from "react"
import { cn } from "@/lib/utils"

// Table scrolls sideways when it doesn't fit, and says so: a fade on the
// edge that has more columns (issue #130). Pin a row's actions with
// stickyEnd so they stay reachable while the rest scrolls.
function Table({ className, ...props }: React.ComponentProps<"table">) {
  const ref = React.useRef<HTMLDivElement>(null)
  const [more, setMore] = React.useState({ start: false, end: false })
  React.useEffect(() => {
    const el = ref.current
    if (!el) return
    const update = () => {
      const max = el.scrollWidth - el.clientWidth
      const next = { start: el.scrollLeft > 1, end: el.scrollLeft < max - 1 }
      setMore((m) => (m.start === next.start && m.end === next.end ? m : next))
    }
    update()
    el.addEventListener("scroll", update, { passive: true })
    const ro = new ResizeObserver(update)
    ro.observe(el)
    if (el.firstElementChild) ro.observe(el.firstElementChild)
    return () => {
      el.removeEventListener("scroll", update)
      ro.disconnect()
    }
  }, [])
  return (
    <div className="relative">
      <div
        ref={ref}
        data-slot="table-container"
        data-more-start={more.start || undefined}
        data-more-end={more.end || undefined}
        className="relative w-full overflow-x-auto"
      >
        <table
          data-slot="table"
          className={cn("w-full caption-bottom text-sm", className)}
          {...props}
        />
      </div>
      <div aria-hidden className={cn("pointer-events-none absolute inset-y-0 left-0 w-6 bg-linear-to-r from-foreground/10 to-transparent transition-opacity", more.start ? "opacity-100" : "opacity-0")} />
      <div aria-hidden className={cn("pointer-events-none absolute inset-y-0 right-0 w-6 bg-linear-to-l from-foreground/10 to-transparent transition-opacity", more.end ? "opacity-100" : "opacity-0")} />
    </div>
  )
}

// stickyEnd keeps a row's last cell (its actions) in view while a wide
// table scrolls.
// Opaque, so scrolled cells don't show through; it follows the row's
// hover and selected backgrounds.
const stickyEnd =
  "sticky right-0 z-[1] bg-card shadow-[-8px_0_8px_-8px_rgb(0_0_0/0.15)] [tr:hover>&]:bg-[color-mix(in_srgb,var(--color-muted)_50%,var(--color-card))] [tr[data-state=selected]>&]:bg-muted"

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  return (
    <thead
      data-slot="table-header"
      className={cn("[&_tr]:border-b", className)}
      {...props}
    />
  )
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
  return (
    <tbody
      data-slot="table-body"
      className={cn("[&_tr:last-child]:border-0", className)}
      {...props}
    />
  )
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
  return (
    <tfoot
      data-slot="table-footer"
      className={cn(
        "border-t bg-muted/50 font-medium [&>tr]:last:border-b-0",
        className
      )}
      {...props}
    />
  )
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted",
        className
      )}
      {...props}
    />
  )
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "h-10 px-2 text-left align-middle font-medium whitespace-nowrap text-foreground [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        className
      )}
      {...props}
    />
  )
}

function TableCell({ className, ...props }: React.ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      className={cn(
        "p-2 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        className
      )}
      {...props}
    />
  )
}

function TableCaption({
  className,
  ...props
}: React.ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("mt-4 text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

export {
  stickyEnd,
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
}
