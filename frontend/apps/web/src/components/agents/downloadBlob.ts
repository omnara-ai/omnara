export function downloadBlob(content: Blob, filename: string) {
  const url = URL.createObjectURL(content)
  try {
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    anchor.click()
  } finally {
    URL.revokeObjectURL(url)
  }
}
