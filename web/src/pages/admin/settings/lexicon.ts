/** 词表行解析：trim、去空行、# 注释、大小写不敏感去重（保留首次出现的写法）。 */
export function parseLexiconText(raw: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const line of raw.split(/\r?\n/)) {
    const t = line.trim()
    if (!t || t.startsWith('#')) continue
    const key = t.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(t)
  }
  return out
}

/** 合并两段词表文本（先 left 后 right），规则同 parseLexiconText。 */
export function mergeLexiconText(left: string, right: string): string {
  return parseLexiconText(`${left}\n${right}`).join('\n')
}

/** 导出为带简短头注释的 .txt */
export function formatLexiconExport(keywords: string[], siteName?: string): string {
  const header = [
    '# OCR keyword lexicon',
    siteName ? `# site: ${siteName}` : null,
    `# count: ${keywords.length}`,
    `# exported: ${new Date().toISOString()}`,
    '# one term per line; lines starting with # are comments',
    '',
  ]
    .filter((x) => x != null)
    .join('\n')
  return header + keywords.join('\n') + (keywords.length ? '\n' : '')
}
