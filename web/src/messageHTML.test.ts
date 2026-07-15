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
})
