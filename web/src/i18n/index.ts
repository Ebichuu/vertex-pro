import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

i18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
  resources: {
    'zh-CN': { translation: { appName: 'Vertex Pro' } },
    en: { translation: { appName: 'Vertex Pro' } },
  },
})

export default i18n
