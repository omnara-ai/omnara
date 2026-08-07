import { cn } from '@/lib/utils'

// Omnara logo mark, mirrored from docs/logo/omnara-logo.svg.
function OmnaraLogo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" fill="none" xmlns="http://www.w3.org/2000/svg" className={className}>
      <g filter="url(#omnara-logo-shadow)">
        <path
          fillRule="evenodd"
          clipRule="evenodd"
          d="M18.11 2.02C10.3 1.56 2.61 8.43 1.9 22.81H6.35C6.35 22.81 6.35 28.95 6.35 29.32C6.35 29.69 6.35 30 6.97 30C7.59 30 17.79 30 17.79 30V26.99C20.6 27.36 29.96 24.12 30.1 15.78C30.23 7.44 25.92 2.48 18.11 2.02ZM17.59 5.16C22.74 5.16 26.92 9.34 26.92 14.49C26.92 19.64 22.94 23.85 17.79 23.85V26.99C9.6 22.77 8.29 20.51 8.26 14.49C8.26 9.34 12.44 5.16 17.59 5.16Z"
          fill="url(#omnara-logo-fill)"
        />
        <path
          d="M10.88 14.56C10.88 15.88 11.94 16.94 13.26 16.94C14.57 16.94 15.63 15.88 15.63 14.56C15.63 13.25 14.57 12.19 13.26 12.19C11.94 12.19 10.88 13.25 10.88 14.56Z"
          fill="url(#omnara-logo-fill)"
        />
      </g>
      <defs>
        <filter
          id="omnara-logo-shadow"
          x="1.9"
          y="2"
          width="28.2"
          height="28"
          filterUnits="userSpaceOnUse"
          colorInterpolationFilters="sRGB"
        >
          <feFlood floodOpacity="0" result="BackgroundImageFix" />
          <feBlend mode="normal" in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
          <feColorMatrix
            in="SourceAlpha"
            type="matrix"
            values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0"
            result="hardAlpha"
          />
          <feOffset dx="-0.4" dy="0.4" />
          <feComposite in2="hardAlpha" operator="arithmetic" k2="-1" k3="1" />
          <feColorMatrix type="matrix" values="0 0 0 0 1 0 0 0 0 1 0 0 0 0 1 0 0 0 0.5 0" />
          <feBlend mode="normal" in2="shape" result="effect1_innerShadow_444_6982" />
        </filter>
        <linearGradient
          id="omnara-logo-fill"
          x1="30.1"
          y1="2"
          x2="1.9"
          y2="30"
          gradientUnits="userSpaceOnUse"
        >
          <stop stopColor="var(--foreground)" />
          <stop offset="1" stopColor="var(--muted-foreground)" />
        </linearGradient>
      </defs>
    </svg>
  )
}

// Transparent brand mark: the logo artwork carries a foreground-token
// gradient fill, so it renders directly on the page background in both
// themes.
export function BrandMark({ className }: { className?: string }) {
  return (
    <span className={cn('flex size-8 shrink-0 items-center justify-center', className)}>
      <OmnaraLogo className="size-full" />
    </span>
  )
}
