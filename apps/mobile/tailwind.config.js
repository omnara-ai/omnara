/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./App.{js,jsx,ts,tsx}", "./src/**/*.{js,jsx,ts,tsx}"],
  theme: {
    extend: {
      colors: {
        primary: {
          DEFAULT: '#3B82F6', // Electric Blue
          light: '#60A5FA',   // Electric Accent
          dark: '#1E3A8A',    // Midnight Blue
        },
        secondary: {
          DEFAULT: '#8B5CF6', // Electric Violet
          light: '#A78BFA',
          dark: '#6D28D9',
        },
        background: {
          DEFAULT: '#1E3A8A', // Midnight Blue
          light: '#F9FAFB',   // Off-White
          dark: '#111827',    // Charcoal
        },
        surface: {
          DEFAULT: '#F9FAFB',
          dark: '#1F2937',
        },
        text: {
          DEFAULT: '#111827', // Charcoal
          light: '#6B7280',
          inverse: '#F9FAFB',
        },
        success: '#10B981',
        warning: '#F59E0B',
        error: '#EF4444',
        info: '#3B82F6',
      },
      fontFamily: {
        sans: ['System', 'sans-serif'],
      },
      spacing: {
        '18': '4.5rem',
        '88': '22rem',
      },
      borderRadius: {
        'xl': '1rem',
        '2xl': '1.5rem',
        '3xl': '2rem',
      },
    },
  },
  plugins: [],
}