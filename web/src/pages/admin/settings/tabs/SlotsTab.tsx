import { useState, type Dispatch, type SetStateAction } from 'react'
import type { ShareBranding, SiteAnnouncement } from '../../../../api/types'
import { useT } from '../../../../i18n'
import { toLocaleMap } from '../../../../lib/locale'
import { Button } from '../../../../ui/Button'
import { Input } from '../../../../ui/Input'
import { Segmented } from '../../../../ui/Segmented'
import { Toggle } from '../../../../ui/Toggle'
import type { FormFooterGroup, FormLocale, FormSet, FormState } from '../settingsForm'
import { emptyLocale } from '../settingsForm'
import { s } from '../settingsUi'

type SlotsSub = 'public' | 'announcement' | 'footer' | 'html'

interface Props {
  form: FormState
  set: FormSet
  setForm: Dispatch<SetStateAction<FormState | null>>
  setAnn: <K extends keyof SiteAnnouncement>(k: K, v: SiteAnnouncement[K]) => void
  patchFooterGroup: (gi: number, patch: Partial<FormFooterGroup>) => void
  patchFooterLink: (
    gi: number,
    li: number,
    patch: { label?: FormLocale; url?: string },
  ) => void
}

function setLocaleField(
  loc: FormLocale | string | { zh?: string; en?: string } | undefined,
  side: 'zh' | 'en',
  v: string,
): FormLocale {
  const m = toLocaleMap(loc)
  return { ...m, [side]: v }
}

export function SlotsTab({ form, set, setForm, setAnn, patchFooterGroup, patchFooterLink }: Props) {
  const { t } = useT()
  const [sub, setSub] = useState<SlotsSub>('public')
  const annText = toLocaleMap(form.ann.text)
  const annLabel = toLocaleMap(form.ann.link_label)

  return (
    <>
      <nav className={s.subTabs} aria-label={t('adminB.slotsTab')}>
        {(
          [
            ['public', 'slotsSubPublic'],
            ['announcement', 'slotsSubAnnouncement'],
            ['footer', 'slotsSubFooter'],
            ['html', 'slotsSubHtml'],
          ] as const
        ).map(([key, labelKey]) => (
          <button
            key={key}
            type="button"
            className={[s.subTab, sub === key && s.subTabActive].filter(Boolean).join(' ')}
            aria-pressed={sub === key}
            onClick={() => setSub(key)}
          >
            {t(`adminB.${labelKey}`)}
          </button>
        ))}
      </nav>

      {sub === 'public' && (
        <section className={s.section}>
          <h2 className={s.h2}>{t('adminB.publicCopy')}</h2>
          <span className={s.hint}>{t('adminB.publicCopyHint')}</span>
          <Input
            label={t('adminB.helpUrl')}
            value={form.helpUrl}
            placeholder="https://… or /path"
            onChange={(e) => set('helpUrl', e.target.value)}
          />
          <Input
            label={t('adminB.upgradeUrl')}
            value={form.upgradeUrl}
            placeholder="https://… or /path"
            onChange={(e) => set('upgradeUrl', e.target.value)}
          />
          <div className={s.localePair}>
            <Input
              label={`${t('adminB.registerNotice')} · ${t('adminB.localeZh')}`}
              value={form.registerNotice.zh}
              maxLength={500}
              onChange={(e) => set('registerNotice', { ...form.registerNotice, zh: e.target.value })}
            />
            <Input
              label={`${t('adminB.registerNotice')} · ${t('adminB.localeEn')}`}
              value={form.registerNotice.en}
              maxLength={500}
              onChange={(e) => set('registerNotice', { ...form.registerNotice, en: e.target.value })}
            />
          </div>
          <span className={s.hint}>{t('adminB.registerNoticeHint')}</span>

          <h2 className={s.h2}>{t('adminB.publicStats')}</h2>
          <span className={s.hint}>{t('adminB.publicStatsHint')}</span>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.publicStatsEnabled')}</span>
              <Toggle
                aria-label={t('adminB.publicStatsEnabled')}
                checked={form.publicStatsEnabled}
                onChange={(v) => set('publicStatsEnabled', v)}
              />
            </div>
          </div>
          <Input
            label={t('adminB.publicStatsSince')}
            value={form.publicStatsSince}
            placeholder="YYYY-MM-DD"
            onChange={(e) => set('publicStatsSince', e.target.value)}
          />
          <span className={s.hint}>{t('adminB.publicStatsSinceHint')}</span>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.publicStatsShowUptime')}</span>
              <Toggle
                aria-label={t('adminB.publicStatsShowUptime')}
                checked={form.publicStatsShowUptime}
                onChange={(v) => set('publicStatsShowUptime', v)}
              />
            </div>
          </div>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.publicStatsShowImages')}</span>
              <Toggle
                aria-label={t('adminB.publicStatsShowImages')}
                checked={form.publicStatsShowImages}
                onChange={(v) => set('publicStatsShowImages', v)}
              />
            </div>
          </div>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.publicStatsShowUsers')}</span>
              <Toggle
                aria-label={t('adminB.publicStatsShowUsers')}
                checked={form.publicStatsShowUsers}
                onChange={(v) => set('publicStatsShowUsers', v)}
              />
            </div>
          </div>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.publicStatsShowBytes')}</span>
              <Toggle
                aria-label={t('adminB.publicStatsShowBytes')}
                checked={form.publicStatsShowBytes}
                onChange={(v) => set('publicStatsShowBytes', v)}
              />
            </div>
          </div>

          <div className={s.field}>
            <span className={s.label}>{t('adminB.shareBranding')}</span>
            <Segmented<ShareBranding>
              options={[
                { value: 'off', label: t('adminB.shareBrandingOff') },
                { value: 'site', label: t('adminB.shareBrandingSite') },
                { value: 'links', label: t('adminB.shareBrandingLinks') },
              ]}
              value={form.shareBranding}
              onChange={(v) => set('shareBranding', v)}
            />
            <span className={s.hint}>{t('adminB.shareBrandingHint')}</span>
          </div>
          <Input
            label={t('adminB.faviconUrl')}
            value={form.faviconUrl}
            placeholder="https://…/favicon.svg"
            onChange={(e) => set('faviconUrl', e.target.value)}
          />
          <span className={s.hint}>{t('adminB.faviconUrlHint')}</span>
          <Input
            label={t('adminB.sourceUrl')}
            value={form.sourceUrl}
            placeholder="https://github.com/… or self-hosted source"
            onChange={(e) => set('sourceUrl', e.target.value)}
          />
          <span className={s.hint}>{t('adminB.sourceUrlHint')}</span>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.aboutEnabled')}</span>
              <Toggle
                aria-label={t('adminB.aboutEnabled')}
                checked={form.aboutEnabled}
                onChange={(v) => set('aboutEnabled', v)}
              />
            </div>
            <span className={s.hint}>{t('adminB.aboutEnabledHint')}</span>
          </div>
          <div className={s.localePair}>
            <div className={s.field}>
              <span className={s.label}>{`${t('adminB.aboutBody')} · ${t('adminB.localeZh')}`}</span>
              <textarea
                className={s.textarea}
                rows={4}
                maxLength={4000}
                value={form.aboutBody.zh}
                onChange={(e) => set('aboutBody', { ...form.aboutBody, zh: e.target.value })}
              />
            </div>
            <div className={s.field}>
              <span className={s.label}>{`${t('adminB.aboutBody')} · ${t('adminB.localeEn')}`}</span>
              <textarea
                className={s.textarea}
                rows={4}
                maxLength={4000}
                value={form.aboutBody.en}
                onChange={(e) => set('aboutBody', { ...form.aboutBody, en: e.target.value })}
              />
            </div>
          </div>
        </section>
      )}

      {sub === 'announcement' && (
        <section className={s.section}>
          <div className={s.h2Row}>
            <h2 className={s.h2}>{t('adminB.announcement')}</h2>
            <Toggle checked={form.ann.enabled} onChange={(v) => setAnn('enabled', v)} />
          </div>
          <span className={s.hint}>{t('adminB.announcementHint')}</span>
          <div className={s.localePair}>
            <Input
              label={`${t('adminB.announcementText')} · ${t('adminB.localeZh')}`}
              value={annText.zh}
              maxLength={500}
              onChange={(e) => setAnn('text', setLocaleField(form.ann.text, 'zh', e.target.value))}
            />
            <Input
              label={`${t('adminB.announcementText')} · ${t('adminB.localeEn')}`}
              value={annText.en}
              maxLength={500}
              onChange={(e) => setAnn('text', setLocaleField(form.ann.text, 'en', e.target.value))}
            />
          </div>
          <Input
            label={t('adminB.announcementLinkUrl')}
            value={form.ann.link_url}
            placeholder="https://… or /path"
            onChange={(e) => setAnn('link_url', e.target.value)}
          />
          <div className={s.localePair}>
            <Input
              label={`${t('adminB.announcementLinkLabel')} · ${t('adminB.localeZh')}`}
              value={annLabel.zh}
              maxLength={80}
              onChange={(e) =>
                setAnn('link_label', setLocaleField(form.ann.link_label, 'zh', e.target.value))
              }
            />
            <Input
              label={`${t('adminB.announcementLinkLabel')} · ${t('adminB.localeEn')}`}
              value={annLabel.en}
              maxLength={80}
              onChange={(e) =>
                setAnn('link_label', setLocaleField(form.ann.link_label, 'en', e.target.value))
              }
            />
          </div>
          <div className={s.field}>
            <div className={s.sliderHead}>
              <span className={s.label}>{t('adminB.announcementDismissible')}</span>
              <Toggle
                aria-label={t('adminB.announcementDismissible')}
                checked={form.ann.dismissible}
                onChange={(v) => setAnn('dismissible', v)}
              />
            </div>
          </div>
          <details className={s.fold}>
            <summary>{t('adminB.announcementSchedule')}</summary>
            <div className={s.foldBody}>
              <Input
                label={t('adminB.announcementStarts')}
                value={form.ann.starts_at}
                placeholder="2026-07-01T00:00:00Z"
                onChange={(e) => setAnn('starts_at', e.target.value)}
              />
              <Input
                label={t('adminB.announcementEnds')}
                value={form.ann.ends_at}
                placeholder="2026-08-01T00:00:00Z"
                onChange={(e) => setAnn('ends_at', e.target.value)}
              />
            </div>
          </details>
        </section>
      )}

      {sub === 'footer' && (
        <section className={s.section}>
          <h2 className={s.h2}>{t('adminB.footerLinks')}</h2>
          <span className={s.hint}>{t('adminB.footerLinksHint')}</span>
          {form.footerGroups.map((g, gi) => (
            <div key={gi} className={s.slotCard}>
              <div className={s.localePair}>
                <Input
                  label={`${t('adminB.footerGroupTitle')} · ${t('adminB.localeZh')}`}
                  value={g.title.zh}
                  maxLength={80}
                  onChange={(e) => patchFooterGroup(gi, { title: { ...g.title, zh: e.target.value } })}
                />
                <Input
                  label={`${t('adminB.footerGroupTitle')} · ${t('adminB.localeEn')}`}
                  value={g.title.en}
                  maxLength={80}
                  onChange={(e) => patchFooterGroup(gi, { title: { ...g.title, en: e.target.value } })}
                />
              </div>
              {g.links.map((l, li) => (
                <div key={li} className={s.slotRow}>
                  <div className={s.localePair}>
                    <Input
                      label={`${t('adminB.footerLinkLabel')} · ${t('adminB.localeZh')}`}
                      value={l.label.zh}
                      maxLength={80}
                      onChange={(e) =>
                        patchFooterLink(gi, li, { label: { ...l.label, zh: e.target.value } })
                      }
                    />
                    <Input
                      label={`${t('adminB.footerLinkLabel')} · ${t('adminB.localeEn')}`}
                      value={l.label.en}
                      maxLength={80}
                      onChange={(e) =>
                        patchFooterLink(gi, li, { label: { ...l.label, en: e.target.value } })
                      }
                    />
                  </div>
                  <Input
                    label={t('adminB.footerLinkUrl')}
                    value={l.url}
                    onChange={(e) => patchFooterLink(gi, li, { url: e.target.value })}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() =>
                      patchFooterGroup(gi, {
                        links: g.links.filter((_, j) => j !== li),
                      })
                    }
                  >
                    {t('adminB.removeLink')}
                  </Button>
                </div>
              ))}
              <div className={s.slotActions}>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() =>
                    patchFooterGroup(gi, {
                      links: [...g.links, { label: emptyLocale(), url: '' }],
                    })
                  }
                >
                  {t('adminB.addLink')}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() =>
                    setForm((f) =>
                      f
                        ? {
                            ...f,
                            footerGroups: f.footerGroups.filter((_, i) => i !== gi),
                          }
                        : f,
                    )
                  }
                >
                  {t('adminB.removeGroup')}
                </Button>
              </div>
            </div>
          ))}
          <Button
            type="button"
            variant="secondary"
            onClick={() =>
              setForm((f) =>
                f
                  ? {
                      ...f,
                      footerGroups: [
                        ...f.footerGroups,
                        { title: emptyLocale(), links: [{ label: emptyLocale(), url: '' }] },
                      ],
                    }
                  : f,
              )
            }
          >
            {t('adminB.addGroup')}
          </Button>
        </section>
      )}

      {sub === 'html' && (
        <section className={s.section}>
          <h2 className={s.h2}>{t('adminB.htmlInject')}</h2>
          <span className={s.hintWarn}>{t('adminB.htmlInjectWarn')}</span>
          <div className={s.field}>
            <span className={s.label}>{t('adminB.htmlHead')}</span>
            <textarea
              className={s.textarea}
              rows={5}
              value={form.htmlHead}
              onChange={(e) => set('htmlHead', e.target.value)}
              spellCheck={false}
            />
          </div>
          <div className={s.field}>
            <span className={s.label}>{t('adminB.htmlBodyEnd')}</span>
            <textarea
              className={s.textarea}
              rows={5}
              value={form.htmlBodyEnd}
              onChange={(e) => set('htmlBodyEnd', e.target.value)}
              spellCheck={false}
            />
          </div>
        </section>
      )}
    </>
  )
}
