export const slackAppNameMaxLength = 35
const slackIconMaxBytes = 5 * 1024 * 1024

/** A selected icon and a rejected selection are mutually exclusive. */
export type AppIcon =
  | { kind: 'none' }
  | { kind: 'checking' }
  | { kind: 'file'; file: File }
  | { kind: 'error'; message: string }

export const noAppIcon: AppIcon = { kind: 'none' }

export interface SlackConnectionFormValues {
  appName: string
  appConfigurationToken: string
  appIcon: AppIcon
}

function fileSizeLabel(bytes: number) {
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export function slackIconRequirements() {
  return `PNG or JPEG, square, 512-2000px, up to ${fileSizeLabel(slackIconMaxBytes)}.`
}

export async function validateAppIcon(file: File | null): Promise<AppIcon> {
  if (!file) return noAppIcon
  if (file.type !== 'image/png' && file.type !== 'image/jpeg') {
    return {
      kind: 'error',
      message: 'App icon must be a PNG or JPEG image.',
    }
  }
  if (file.size > slackIconMaxBytes) {
    return {
      kind: 'error',
      message: `App icon must be ${fileSizeLabel(slackIconMaxBytes)} or smaller.`,
    }
  }
  try {
    const image = await createImageBitmap(file)
    const { width, height } = image
    image.close()
    if (width !== height) {
      return {
        kind: 'error',
        message: `App icon must be square. This image is ${width} × ${height} pixels.`,
      }
    }
    if (width < 512 || width > 2000) {
      return {
        kind: 'error',
        message: 'App icon must be between 512 × 512 and 2000 × 2000 pixels.',
      }
    }
    return { kind: 'file', file }
  } catch {
    return { kind: 'error', message: 'Could not read this image. Choose a valid PNG or JPEG.' }
  }
}

export function readFileBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result
      if (result === null || result instanceof ArrayBuffer) {
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

export function slackConnectionFormValid(values: SlackConnectionFormValues) {
  // Rejected icons are not attached. Only an unfinished check blocks this optional upload.
  return (
    values.appName.trim() !== '' &&
    Array.from(values.appName.trim()).length <= slackAppNameMaxLength &&
    values.appConfigurationToken.trim() !== '' &&
    values.appIcon.kind !== 'checking'
  )
}

export async function slackAppIconPayload(icon: AppIcon) {
  if (icon.kind !== 'file') return undefined
  return { filename: icon.file.name, data_base64: await readFileBase64(icon.file) }
}
