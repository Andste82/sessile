// Upload for the file browser (§4.10, §6) — deliberately not routed through
// client.ts's fetch-based request(): fetch has no upload-progress event
// (only XHR's upload.onprogress does), and a real progress bar for a
// file upload is the whole point of using this instead of a plain PUT.

/** Thrown when an upload ends because abort() was called, not because it failed. */
export class UploadAbortedError extends Error {
  constructor() {
    super('upload cancelled')
    this.name = 'UploadAbortedError'
  }
}

/**
 * A started upload. `promise` settles when the transfer does; `abort` stops it.
 *
 * Aborting the request *is* the cancel operation — there is no cancel endpoint
 * and none is needed. When the request body dies mid-transfer the server's
 * io.Copy fails, its error path removes the `.part` staging file (through a
 * context.WithoutCancel, so the cleanup survives the cancelled request), and no
 * partial file is left at the destination.
 */
export interface UploadHandle {
  promise: Promise<void>
  abort: () => void
}

export function uploadHostFile(
  sessionId: string,
  path: string,
  file: File | Blob,
  onProgress?: (loaded: number, total: number) => void,
): UploadHandle {
  const xhr = new XMLHttpRequest()
  let aborted = false

  const promise = new Promise<void>((resolve, reject) => {
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
    // abort() fires this too, so it has to tell the two apart: a cancelled
    // upload is not a failed one and must not be reported as an error.
    xhr.onerror = () => reject(aborted ? new UploadAbortedError() : new Error('upload failed'))
    xhr.onabort = () => reject(new UploadAbortedError())
    xhr.send(file)
  })

  return {
    promise,
    abort: () => {
      aborted = true
      xhr.abort()
    },
  }
}

/** Build the download URL for one file — a plain same-origin GET the
 * browser fetches natively via an `<a download>`, so its own download
 * manager (and progress UI) handles the transfer; no JS needed here. */
export function hostFileDownloadURL(sessionId: string, path: string): string {
  return `/api/sessions/${sessionId}/hostops/download?path=${encodeURIComponent(path)}`
}
