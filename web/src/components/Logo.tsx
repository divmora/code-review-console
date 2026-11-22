import { cn } from "@/lib/utils";

// Logo is the Divmora Code Review mark: code brackets with a check (the
// same drawing as docs/brand/logo.svg and logo-dark.svg).
export function Logo({ className, title }: { className?: string; title?: string }) {
  return (
    <svg viewBox="0 0 32 32" fill="none" className={cn("size-8 shrink-0", className)} role={title ? "img" : undefined} aria-hidden={title ? undefined : true}>
      {title && <title>{title}</title>}
      {/* Lighter slate tile with an outline in dark mode, so it stays visible. */}
      <rect x="0.5" y="0.5" width="31" height="31" rx="6.5" className="fill-[#0F172A] stroke-[#0F172A] dark:fill-[#1E293B] dark:stroke-[#334155]" />
      <path d="M10 10 5 16l5 6M22 10l5 6-5 6" stroke="#60A5FA" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
      <path d="m12 16.5 3 3 5.5-7" stroke="#FFFFFF" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
