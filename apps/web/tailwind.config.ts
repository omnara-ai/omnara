import type { Config } from "tailwindcss";
import tailwindcssAnimate from "tailwindcss-animate";
import { colors, gradients } from "./src/lib/theme/colors";

export default {
	darkMode: ["class"],
	content: [
		"./pages/**/*.{ts,tsx}",
		"./components/**/*.{ts,tsx}",
		"./app/**/*.{ts,tsx}",
		"./src/**/*.{ts,tsx}",
	],
	prefix: "",
	theme: {
		container: {
			center: true,
			padding: '2rem',
			screens: {
				'2xl': '1400px'
			}
		},
			extend: {
			colors: {
				// Shadcn/ui system colors (CSS variables)
				border: 'hsl(var(--border))',
				input: 'hsl(var(--input))',
				ring: 'hsl(var(--ring))',
				background: 'hsl(var(--background))',
				foreground: 'hsl(var(--foreground))',
				
				// Minimalist charcoal theme colors
				'background-base': 'hsl(var(--color-background-base))',
				'surface-panel': 'hsl(var(--color-surface-panel))',
				'text-primary': 'hsl(var(--color-text-primary))',
				'text-secondary': 'hsl(var(--color-text-secondary))',
				'border-divider': 'hsl(var(--color-border-divider))',
				'interactive-hover': 'hsl(var(--color-interactive-hover))',
				'functional-positive': 'hsl(var(--color-functional-positive))',
				'functional-negative': 'hsl(var(--color-functional-negative))',
				// Diff tokens for chat and panels
				'diff-add-bg': 'hsl(var(--diff-add-bg))',
				'diff-add-text': 'hsl(var(--diff-add-text))',
				'diff-del-bg': 'hsl(var(--diff-del-bg))',
				'diff-del-text': 'hsl(var(--diff-del-text))',
				primary: {
					DEFAULT: 'hsl(var(--primary))',
					foreground: 'hsl(var(--primary-foreground))'
				},
				secondary: {
					DEFAULT: 'hsl(var(--secondary))',
					foreground: 'hsl(var(--secondary-foreground))'
				},
				destructive: {
					DEFAULT: 'hsl(var(--destructive))',
					foreground: 'hsl(var(--destructive-foreground))'
				},
				muted: {
					DEFAULT: 'hsl(var(--muted))',
					foreground: 'hsl(var(--muted-foreground))'
				},
				accent: {
					DEFAULT: 'hsl(var(--accent))',
					foreground: 'hsl(var(--accent-foreground))'
				},
				popover: {
					DEFAULT: 'hsl(var(--popover))',
					foreground: 'hsl(var(--popover-foreground))'
				},
				card: {
					DEFAULT: 'hsl(var(--card))',
					foreground: 'hsl(var(--card-foreground))'
				},
				sidebar: {
					DEFAULT: 'hsl(var(--sidebar-background))',
					foreground: 'hsl(var(--sidebar-foreground))',
					primary: 'hsl(var(--sidebar-primary))',
					'primary-foreground': 'hsl(var(--sidebar-primary-foreground))',
					accent: 'hsl(var(--sidebar-accent))',
					'accent-foreground': 'hsl(var(--sidebar-accent-foreground))',
					border: 'hsl(var(--sidebar-border))',
					ring: 'hsl(var(--sidebar-ring))'
				},

				// Centralized Omnara Brand Colors
				...colors.brand,
				...colors.legacy,
				...colors.omnara,

				// Semantic color scales
				success: colors.semantic.success,
				warning: colors.semantic.warning,
				error: colors.semantic.error,
				info: colors.semantic.info,
				neutral: colors.neutral,

				// Additional aliases for common usage
				'background-alt': colors.brand['warm-midnight'],
				'text-primary': colors.neutral[50],
				'text-secondary': colors.neutral[400],
				'text-muted': colors.neutral[500],
				
				// Brand-specific aliases
				'omnara-gold': colors.omnara.gold,
				'omnara-gold-light': colors.omnara['gold-light'],
				'omnara-cream-text': colors.omnara['cream-text'],
				'claude-purple': '#D97757',
			},
			backgroundImage: {
				// Centralized gradients
				...Object.fromEntries(
					Object.entries(gradients).map(([key, value]) => [key, value])
				),
				// Overlay helpers
				vignette: 'radial-gradient(ellipse at center, transparent 0%, rgba(26, 22, 24, 0.4) 60%, rgba(26, 22, 24, 0.7) 100%)',
				'top-fade': 'linear-gradient(to bottom, rgba(30, 27, 41, 0.7) 0%, transparent 25%)',
				// Standard Tailwind gradient utility
				'radial-gradient': 'radial-gradient(ellipse at center, var(--tw-gradient-stops))',
			},
			borderRadius: {
				lg: 'var(--radius)',
				md: 'calc(var(--radius) - 2px)',
				sm: 'calc(var(--radius) - 4px)'
			},
			keyframes: {
				'accordion-down': {
					from: {
						height: '0'
					},
					to: {
						height: 'var(--radix-accordion-content-height)'
					}
				},
				'accordion-up': {
					from: {
						height: 'var(--radix-accordion-content-height)'
					},
					to: {
						height: '0'
					}
				},
				'fade-in': {
					'0%': {
						opacity: '0',
						transform: 'translateY(20px)'
					},
					'100%': {
						opacity: '1',
						transform: 'translateY(0)'
					}
				},
				'slide-in-right': {
					'0%': {
						transform: 'translateX(100px)',
						opacity: '0'
					},
					'100%': {
						transform: 'translateX(0)',
						opacity: '1'
					}
				},
				'float': {
					'0%, 100%': {
						transform: 'translateY(0px)'
					},
					'50%': {
						transform: 'translateY(-10px)'
					}
				},
				// Fixed light beam animations - starting completely off-screen with no delays
				'light-beam-1': {
					'0%': {
						transform: 'translateX(-300px) skewX(-15deg)',
						opacity: '0'
					},
					'5%': {
						opacity: '1'
					},
					'95%': {
						opacity: '1'
					},
					'100%': {
						transform: 'translateX(calc(100vw + 300px)) skewX(-15deg)',
						opacity: '0'
					}
				},
				'light-beam-2': {
					'0%': {
						transform: 'translateX(-300px) skewX(-15deg)',
						opacity: '0'
					},
					'5%': {
						opacity: '1'
					},
					'95%': {
						opacity: '1'
					},
					'100%': {
						transform: 'translateX(calc(100vw + 300px)) skewX(-15deg)',
						opacity: '0'
					}
				},
				'light-beam-3': {
					'0%': {
						transform: 'translateX(-300px) skewX(-15deg)',
						opacity: '0'
					},
					'5%': {
						opacity: '1'
					},
					'95%': {
						opacity: '1'
					},
					'100%': {
						transform: 'translateX(calc(100vw + 300px)) skewX(-15deg)',
						opacity: '0'
					}
				},
				// Slowed down Aurora-like animations - now 12-20 seconds for much smoother movement
				'aurora-drift-1': {
					'0%': {
						transform: 'translate(-20%, -10%) scale(1) rotate(0deg)',
						borderRadius: '60% 40% 30% 70%'
					},
					'25%': {
						transform: 'translate(10%, 20%) scale(1.1) rotate(90deg)',
						borderRadius: '30% 60% 70% 40%'
					},
					'50%': {
						transform: 'translate(30%, -5%) scale(0.9) rotate(180deg)',
						borderRadius: '70% 30% 40% 60%'
					},
					'75%': {
						transform: 'translate(-10%, 30%) scale(1.05) rotate(270deg)',
						borderRadius: '40% 70% 60% 30%'
					},
					'100%': {
						transform: 'translate(-20%, -10%) scale(1) rotate(360deg)',
						borderRadius: '60% 40% 30% 70%'
					}
				},
				'aurora-drift-2': {
					'0%': {
						transform: 'translate(20%, 10%) scale(1.1) rotate(45deg)',
						borderRadius: '50% 60% 40% 30%'
					},
					'33%': {
						transform: 'translate(-15%, -20%) scale(0.8) rotate(180deg)',
						borderRadius: '70% 30% 60% 40%'
					},
					'66%': {
						transform: 'translate(25%, 35%) scale(1.2) rotate(270deg)',
						borderRadius: '30% 70% 50% 60%'
					},
					'100%': {
						transform: 'translate(20%, 10%) scale(1.1) rotate(405deg)',
						borderRadius: '50% 60% 40% 30%'
					}
				},
				'aurora-drift-3': {
					'0%': {
						transform: 'translate(0%, 20%) scale(0.9) rotate(60deg)',
						borderRadius: '40% 50% 60% 30%'
					},
					'40%': {
						transform: 'translate(-25%, -10%) scale(1.15) rotate(200deg)',
						borderRadius: '60% 40% 30% 70%'
					},
					'80%': {
						transform: 'translate(15%, 40%) scale(0.85) rotate(320deg)',
						borderRadius: '30% 60% 50% 40%'
					},
					'100%': {
						transform: 'translate(0%, 20%) scale(0.9) rotate(420deg)',
						borderRadius: '40% 50% 60% 30%'
					}
				},
				'aurora-morph-1': {
					'0%': {
						transform: 'translate(-5%, -5%) scale(1) rotateX(0deg) rotateY(0deg)',
						borderRadius: '65% 35% 25% 75%',
						opacity: '0.12'
					},
					'20%': {
						transform: 'translate(15%, 25%) scale(1.3) rotateX(20deg) rotateY(90deg)',
						borderRadius: '25% 75% 65% 35%',
						opacity: '0.08'
					},
					'50%': {
						transform: 'translate(-20%, 10%) scale(0.7) rotateX(40deg) rotateY(180deg)',
						borderRadius: '75% 25% 35% 65%',
						opacity: '0.15'
					},
					'80%': {
						transform: 'translate(25%, -15%) scale(1.1) rotateX(60deg) rotateY(270deg)',
						borderRadius: '35% 65% 75% 25%',
						opacity: '0.10'
					},
					'100%': {
						transform: 'translate(-5%, -5%) scale(1) rotateX(80deg) rotateY(360deg)',
						borderRadius: '65% 35% 25% 75%',
						opacity: '0.12'
					}
				},
				'aurora-morph-2': {
					'0%': {
						transform: 'translate(10%, 15%) scale(1.2) skew(5deg, 10deg)',
						borderRadius: '55% 45% 35% 65%',
						opacity: '0.10'
					},
					'30%': {
						transform: 'translate(-30%, -25%) scale(0.8) skew(-10deg, 5deg)',
						borderRadius: '35% 65% 55% 45%',
						opacity: '0.15'
					},
					'60%': {
						transform: 'translate(20%, 30%) scale(1.4) skew(15deg, -5deg)',
						borderRadius: '65% 35% 45% 55%',
						opacity: '0.08'
					},
					'100%': {
						transform: 'translate(10%, 15%) scale(1.2) skew(5deg, 10deg)',
						borderRadius: '55% 45% 35% 65%',
						opacity: '0.10'
					}
				},
				'fadeIn': {
					'from': {
						opacity: '0',
						transform: 'translateY(10px)'
					},
					'to': {
						opacity: '1',
						transform: 'translateY(0)'
					}
				},
				'scaleIn': {
					'from': {
						opacity: '0',
						transform: 'scale(0.8)'
					},
					'to': {
						opacity: '1',
						transform: 'scale(1)'
					}
				},
				// New Ghibli-inspired animations
				'twinkle': {
					'0%, 100%': {
						opacity: '0.3',
						transform: 'scale(1)'
					},
					'50%': {
						opacity: '1',
						transform: 'scale(1.2)'
					}
				},
				'scan-line': {
					'0%': { transform: 'translateY(-100%)' },
					'100%': { transform: 'translateY(100%)' }
				}
			},
			animation: {
				'accordion-down': 'accordion-down 0.2s ease-out',
				'accordion-up': 'accordion-up 0.2s ease-out',
				'fade-in': 'fade-in 0.6s ease-out',
				'slide-in-right': 'slide-in-right 0.8s ease-out',
				'float': 'float 3s ease-in-out infinite',
				// Fixed beam animations - removed all delays, staggered with different start positions
				'light-beam-1': 'light-beam-1 20.7s linear infinite',
				'light-beam-2': 'light-beam-2 25.3s linear infinite',
				'light-beam-3': 'light-beam-3 29.9s linear infinite',
				// Slowed down Aurora animations - now 12-20 seconds for smooth, visible movement
				'aurora-drift-1': 'aurora-drift-1 18s ease-in-out infinite',
				'aurora-drift-2': 'aurora-drift-2 15s ease-in-out infinite',
				'aurora-drift-3': 'aurora-drift-3 20s ease-in-out infinite',
				'aurora-morph-1': 'aurora-morph-1 12s ease-in-out infinite',
				'aurora-morph-2': 'aurora-morph-2 16s ease-in-out infinite',
				'fadeIn': 'fadeIn 0.5s ease-out forwards',
				'scaleIn': 'scaleIn 0.5s ease-out forwards',
				// New Ghibli animations
				'twinkle': 'twinkle 3s ease-in-out infinite',
				'scan-line': 'scan-line 8s linear infinite'
			}
		}
	},
	plugins: [tailwindcssAnimate],
} satisfies Config;
