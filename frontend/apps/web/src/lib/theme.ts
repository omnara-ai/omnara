// Theme preference handling. index.html applies the initial class inline
// (before first paint); this module owns changes after load. 'system' is
// stored as an absent key so a fresh browser follows the OS setting.
export type ThemePreference = 'light' | 'dark' | 'system'

const media = window.matchMedia('(prefers-color-scheme: dark)')

export function getThemePreference(): ThemePreference {
  const stored = localStorage.getItem('theme')
  return stored === 'light' || stored === 'dark' ? stored : 'system'
}

function syncDocument() {
  const preference = getThemePreference()
  const dark = preference === 'dark' || (preference === 'system' && media.matches)
  document.documentElement.classList.toggle('dark', dark)
}

export function setThemePreference(preference: ThemePreference) {
  if (preference === 'system') localStorage.removeItem('theme')
  else localStorage.setItem('theme', preference)
  syncDocument()
}

media.addEventListener('change', syncDocument)
