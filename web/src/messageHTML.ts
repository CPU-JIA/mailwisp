import DOMPurify from 'dompurify'

// buildSafeMessageDocument creates an isolated iframe document that cannot
// execute scripts, submit forms, or make external network requests.
export function buildSafeMessageDocument(html: string, cidSources: Record<string, string> = {}): string {
  const inert = new DOMParser().parseFromString(html, 'text/html')
  const cidRegistry: string[] = []
  for (const image of inert.querySelectorAll('img[src]')) {
    const source = image.getAttribute('src')
    if (source?.startsWith('cid:')) {
      image.setAttribute('src', `#mailwisp-cid-${cidRegistry.push(normalizeCID(source.slice(4)))}`)
    }
  }
  const sanitized = DOMPurify.sanitize(`<div>${inert.body.innerHTML}</div>`, {
    FORBID_TAGS: ['script', 'style', 'form', 'input', 'button', 'video', 'audio', 'object', 'embed'],
    FORBID_ATTR: ['srcset', 'ping', 'autofocus'],
  })
  const parser = new DOMParser()
  const document = parser.parseFromString(sanitized, 'text/html')
  for (const element of document.querySelectorAll('script, style, form, input, button, video, audio, object, embed')) {
    element.remove()
  }
  for (const image of document.querySelectorAll('img[src^="#mailwisp-cid-"]')) {
    const index = Number.parseInt((image.getAttribute('src') || '').replace(/^#mailwisp-cid-/, ''), 10)
    const cid = Number.isSafeInteger(index) && index > 0 ? cidRegistry[index - 1] || '' : ''
    const source = cidSources[cid]
    if (source?.startsWith('data:image/')) image.setAttribute('src', source)
    else {
      image.removeAttribute('src')
      image.setAttribute('data-mailwisp-blocked', 'true')
    }
  }
  for (const element of document.querySelectorAll('[src], [href], [action], [poster], [background]')) {
    for (const attribute of ['src', 'href', 'action', 'poster', 'background']) {
      const value = element.getAttribute(attribute)
      if (!value) continue
      if (attribute === 'src' && element.tagName === 'IMG') {
        if (value.startsWith('data:image/')) {
          continue
        }
      } else if (attribute === 'href' && value.startsWith('#')) {
        continue
      }
      element.removeAttribute(attribute)
      element.setAttribute('data-mailwisp-blocked', 'true')
    }
  }
  const body = document.body.innerHTML
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline';"><style>body{margin:0;padding:24px;color:#20231f;background:#fbfaf5;font:16px/1.65 Georgia,'Times New Roman',serif;overflow-wrap:anywhere}img{max-width:100%;height:auto}a{color:#164f7a}blockquote{margin-left:0;padding-left:16px;border-left:2px solid #a7b4bd}pre{white-space:pre-wrap}[data-mailwisp-blocked]{opacity:.7}</style></head><body>${body}</body></html>`
}

export function normalizeCID(value: string): string {
  const trimmed = value.trim().replace(/^<|>$/g, '')
  try {
    return decodeURIComponent(trimmed).toLowerCase()
  } catch {
    return trimmed.toLowerCase()
  }
}
