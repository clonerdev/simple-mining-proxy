/** Offers a text as a file to save: the browser's download, with no request to the server. */
export function downloadText(filename: string, text: string, type = 'text/csv;charset=utf-8'): void {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  // Not at once: some browsers start the download after the click returns.
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}
