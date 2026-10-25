import { useCallback, useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'
export type Language = 'zh-CN' | 'en'

const THEME_KEY = 'vertex.theme'
const LANGUAGE_KEY = 'vertex.language'

function readStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key)
  } catch {
    return null
  }
}

// useTheme 管理亮/暗主题：持久化到 localStorage 并同步到
// documentElement.dataset.theme，供 CSS 变量切换。
export function useTheme(): { theme: Theme; toggleTheme: () => void } {
  const [theme, setTheme] = useState<Theme>(() => (readStored(THEME_KEY) === 'dark' ? 'dark' : 'light'))
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    document.documentElement.classList.toggle('dark', theme === 'dark')
    try {
      window.localStorage.setItem(THEME_KEY, theme)
    } catch {
      /* 存储不可用时仅会话内生效 */
    }
  }, [theme])
  const toggleTheme = useCallback(() => {
    setTheme((current) => (current === 'dark' ? 'light' : 'dark'))
  }, [])
  return { theme, toggleTheme }
}

// useLanguagePreference 管理界面语言并持久化。
export function useLanguagePreference(): { language: Language; toggleLanguage: () => void } {
  const [language, setLanguage] = useState<Language>(() => (readStored(LANGUAGE_KEY) === 'en' ? 'en' : 'zh-CN'))
  useEffect(() => {
    try {
      window.localStorage.setItem(LANGUAGE_KEY, language)
    } catch {
      /* 会话内生效 */
    }
  }, [language])
  const toggleLanguage = useCallback(() => {
    setLanguage((current) => (current === 'zh-CN' ? 'en' : 'zh-CN'))
  }, [])
  return { language, toggleLanguage }
}
