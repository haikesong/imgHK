import '@testing-library/jest-dom/vitest'

// Node ≥22 在 globalThis 上自带一个 localStorage 键（未加 --localstorage-file 时为
// undefined）。vitest 的 jsdom 环境只补不覆盖全局键，于是 localStorage 在测试里
// 是 undefined。这里用内存实现兜底；Node 20（CI）与浏览器不受影响。
if (!globalThis.localStorage) {
  const data = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (k: string) => (data.has(k) ? data.get(k)! : null),
      setItem: (k: string, v: string) => void data.set(k, String(v)),
      removeItem: (k: string) => void data.delete(k),
      clear: () => data.clear(),
      key: (i: number) => [...data.keys()][i] ?? null,
      get length() {
        return data.size
      },
    },
  })
}

// 锁 vitest 默认 locale 为 zh，避免 jsdom navigator.language=en-US 让中文断言失败
localStorage.setItem('imgli-lang', 'zh')

// jsdom 无 matchMedia；store.initialTheme 依赖它
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {
        return false
      },
    }) as unknown as MediaQueryList,
})

// jsdom 不实现 Blob URL；上传队列用它生成本地缩略图预览
if (!URL.createObjectURL) {
  URL.createObjectURL = (() => 'blob:mock') as typeof URL.createObjectURL
}
if (!URL.revokeObjectURL) {
  URL.revokeObjectURL = (() => {}) as typeof URL.revokeObjectURL
}
