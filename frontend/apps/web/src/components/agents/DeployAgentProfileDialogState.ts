export const slackAppNameMaxLength = 35
const slackIconMaxBytes = 5 * 1024 * 1024

/** A selected icon and a rejected selection are mutually exclusive. */
export type AppIcon =
  | { kind: 'none' }
  | { kind: 'file'; file: File }
  | { kind: 'error'; message: string }

export const noAppIcon: AppIcon = { kind: 'none' }

export interface DeployFormValues {
  provider: string
  appName: string
  appConfigurationToken: string
  appIcon: AppIcon
}

export function defaultAppName(profileName: string) {
  const trimmed = profileName.trim() || 'Omnara Agent'
  return Array.from(trimmed).slice(0, slackAppNameMaxLength).join('')
}

export function fileSizeLabel(bytes: number) {
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function slackIconRequirements() {
  return `PNG or JPEG, square, 512-2000px, up to ${fileSizeLabel(slackIconMaxBytes)}.`
}

export function validateAppIcon(file: File | null): AppIcon {
  if (!file) return noAppIcon
  if (file.type !== 'image/png' && file.type !== 'image/jpeg') {
    return {
      kind: 'error',
      message: `App icon must be a PNG or JPEG image. ${slackIconRequirements()}`,
    }
  }
  if (file.size > slackIconMaxBytes) {
    return {
      kind: 'error',
      message: `App icon must be ${fileSizeLabel(slackIconMaxBytes)} or smaller. ${slackIconRequirements()}`,
    }
  }
  return { kind: 'file', file }
}

export function readFileBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result
      if (typeof result !== 'string') {
        reject(new Error('Could not read icon file'))
        return
      }
      const data = result.split(',', 2)[1]
      if (!data) {
        reject(new Error('Could not read icon file'))
        return
      }
      resolve(data)
    }
    reader.onerror = () => {
      reject(reader.error ?? new Error('Could not read icon file'))
    }
    reader.readAsDataURL(file)
  })
}

export function deployFormValid(values: DeployFormValues) {
  return (
    values.provider === 'slack' &&
    values.appName.trim() !== '' &&
    Array.from(values.appName.trim()).length <= slackAppNameMaxLength &&
    values.appConfigurationToken.trim() !== '' &&
    values.appIcon.kind !== 'error'
  )
}
