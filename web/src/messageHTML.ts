import DOMPurify from 'dompurify'

// buildSafeMessageDocument creates an isolated iframe document that cannot
// execute scripts, submit forms, or make external network requests.
export function buildSafeMessageDocument(html: string): string {
  const sanitized = DOMPurify.sanitize(html, {
    FORBID_TAGS: ['script', 'style', 'form', 'input', 'button', 'video', 'audio', 'object', 'embed'],
    FORBID_ATTR: ['srcset', 'ping', 'autofocus'],
  })
  const parser = new DOMParser()
  const document = parser.parseFromString(sanitized, 'text/html')
  for (const element of document.querySelectorAll('script, style, form, input, button, video, audio, object, embed')) {
    element.remove()
  }
  for (const element of document.querySelectorAll('[src], [href], [action], [poster], [background]')) {
    for (const attribute of ['src', 'href', 'action', 'poster', 'background']) {
      const value = element.getAttribute(attribute)
      if (!value) continue
      if (!value.startsWith('data:') && !value.startsWith('cid:') && !value.startsWith('#')) {
        element.removeAttribute(attribute)
        element.setAttribute('data-mailwisp-blocked', 'true')
      }
    }
  }
  const body = document.body.innerHTML
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data: cid:; style-src 'unsafe-inline';"><style>body{margin:0;padding:24px;color:#20231f;background:#fbfaf5;font:16px/1.65 Georgia,'Times New Roman',serif;overflow-wrap:anywhere}img{max-width:100%;height:auto}a{color:#164f7a}blockquote{margin-left:0;padding-left:16px;border-left:2px solid #a7b4bd}pre{white-space:pre-wrap}[data-mailwisp-blocked]{opacity:.7}</style></head><body>${body}</body></html>`
}
