import { useTranslation } from 'react-i18next'
import type { ShareRule } from '../../api/types'

/** A share rule in words: "5%", "30 min/day", "left out"; the fallback when there is none. */
export function ShareText({ rule, fallback }: { rule?: ShareRule; fallback: string }) {
  const { t } = useTranslation()
  if (!rule) return <span className="muted">{fallback}</span>
  if (rule.amount === 0) return <span className="badge">{t('share.rules.leftOutShort')}</span>
  return <span>{t(`share.rules.text.${rule.kind}`, { value: rule.amount })}</span>
}
