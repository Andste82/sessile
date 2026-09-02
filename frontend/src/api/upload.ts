// Upload for the file browser (§4.10, §6) — deliberately not routed through
// client.ts's fetch-based request(): fetch has no upload-progress event
// (only XHR's upload.onprogress does), and a real progress bar for a
// file upload is the whole point of using this instead of a plain PUT.
export function uploadHostFile(
  sessionId: string,
  path: string,
  file: File | Blob,
  onProgress?: (loaded: number, total: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/sessions/${sessionId}/hostops/upload?path=${encodeURIComponent(path)}`)
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve()
        return
      }
      let message = `upload failed (${xhr.status})`
      try {
        const body = JSON.parse(xhr.responseText) as { error?: { message?: string } }
        if (body.error?.message) message = body.error.message
      } catch {
        // Non-JSON body (e.g. a proxy's own error page) — keep the status-only message.
      }
      reject(new Error(message))
    }
    xhr.onerror = () => reject(new Error('upload failed'))
    xhr.send(file)
  })
}

/** Build the download URL for one file — a plain same-origin GET the
 * browser fetches natively via an `<a download>`, so its own download
 * manager (and progress UI) handles the transfer; no JS needed here. */
export function hostFileDownloadURL(sessionId: string, path: string): string {
  return `/api/sessions/${sessionId}/hostops/download?path=${encodeURIComponent(path)}`
}
