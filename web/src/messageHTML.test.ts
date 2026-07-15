import { buildSafeMessageDocument } from './messageHTML'

describe('buildSafeMessageDocument', () => {
  it('removes active content and remote tracking resources', () => {
    const document = buildSafeMessageDocument('<script>alert(1)</script><form action="https://evil.test"><img src="https://tracker.test/pixel"><a href="https://evil.test">open</a></form>')
    expect(document).not.toContain('<script')
    expect(document).not.toContain('<form')
    expect(document).not.toContain('https://tracker.test')
    expect(document).not.toContain('https://evil.test')
    expect(document).toContain("default-src 'none'")
  })

  it('replaces known CID images with bounded parent-provided data URLs', () => {
    const source = buildSafeMessageDocument('<img src="cid:Logo%40Example.COM"><img src="cid:missing">', {
      'logo@example.com': 'data:image/png;base64,aW1hZ2U=',
    })
    expect(source).toContain('data:image/png;base64,aW1hZ2U=')
    expect(source).not.toContain('cid:Logo')
    expect(source).not.toContain('cid:missing')
    expect(source).toContain('data-mailwisp-blocked')
    expect(source).toContain("img-src data:")
    expect(source).not.toContain('img-src data: cid:')
  })
})
