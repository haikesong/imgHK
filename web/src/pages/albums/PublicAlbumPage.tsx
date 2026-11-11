import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useConfig } from '../../api/hooks'
import { useT } from '../../i18n'
import { Button } from '../../ui/Button'
import { EmptyState } from '../../ui/EmptyState'
import { Input } from '../../ui/Input'
import { Segmented } from '../../ui/Segmented'
import { ShareBrandFooter } from '../../ui/ShareBrandFooter'
import { AlbumImmersive } from './AlbumImmersive'
import type { AlbumPublicMode } from './albumPublicView'
import { PublicAlbumHero } from './PublicAlbumHero'
import { PublicAlbumMasonry } from './PublicAlbumMasonry'
import { useAlbumViewMode } from './useAlbumViewMode'
import { usePublicAlbum } from './usePublicAlbum'

/** 公开相册访客页 /a/:id —— 瀑布流默认 + 沉浸（URL + 属主 default_view）。 */
export function PublicAlbumPage() {
  const { id = '' } = useParams()
  const { t } = useT()
  const cfg = useConfig()
  const { meta, imgs, rows, notFound, locked, unlock } = usePublicAlbum(id)
  const [gatePw, setGatePw] = useState('')
  const [gateErr, setGateErr] = useState('')

  const {
    activeIndex,
    mode,
    immersive,
    openImmersive,
    closeImmersive,
    goPrev,
    goNext,
    selectIndex,
  } = useAlbumViewMode({
    defaultView: meta.data?.default_view,
    rowsLen: rows.length,
    hasNextPage: !!imgs.hasNextPage,
    isFetchingNextPage: imgs.isFetchingNextPage,
    fetchNextPage: () => {
      void imgs.fetchNextPage()
    },
    ready: !!meta.data && rows.length > 0,
  })

  // document.title：相册名 · 站名
  useEffect(() => {
    const albumName = meta.data?.name?.trim()
    if (!albumName) return
    const site = (cfg.data?.site_name || '').trim()
    const prev = document.title
    document.title = site ? `${albumName} · ${site}` : albumName
    return () => {
      document.title = prev
    }
  }, [meta.data?.name, cfg.data?.site_name])

  if (meta.isLoading) {
    return <div className="px-4 py-12 text-center text-muted">{t('discover.loading')}</div>
  }
  if (notFound) {
    return (
      <div className="flex flex-col items-center gap-4 px-4 py-12 text-center text-muted">
        <EmptyState title={t('albums.publicNotFound')} />
        <Link to="/">
          <Button variant="primary">{t('share.uploadCta')}</Button>
        </Link>
      </div>
    )
  }
  if (meta.isError || !meta.data) {
    return <div className="px-4 py-12 text-center text-muted">{t('share.loadFailed')}</div>
  }

  if (locked) {
    return (
      <div className="mx-auto flex min-h-[50vh] max-w-md flex-col items-center justify-center gap-4 px-4 py-16 text-center">
        <h1 className="m-0 text-xl font-bold">{meta.data.name}</h1>
        <p className="m-0 text-[13px] text-muted">{t('albums.passwordGateHint')}</p>
        <form
          className="flex w-full flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            setGateErr('')
            unlock.mutate(gatePw, {
              onError: (err) => {
                if (err instanceof ApiError && err.httpStatus === 401) setGateErr(t('albums.passwordWrong'))
                else setGateErr(t('albums.unlockFailed'))
              },
            })
          }}
        >
          <Input
            type="password"
            value={gatePw}
            onChange={(e) => setGatePw(e.target.value)}
            placeholder={t('albums.passwordGateTitle')}
            autoFocus
          />
          {gateErr && <div className="text-left text-[12px] text-err">{gateErr}</div>}
          <Button type="submit" variant="primary" disabled={unlock.isPending || !gatePw.trim()}>
            {t('albums.passwordSubmit')}
          </Button>
        </form>
      </div>
    )
  }

  const canPrev = immersive && activeIndex != null && activeIndex > 0
  const canNext =
    immersive &&
    activeIndex != null &&
    (activeIndex < rows.length - 1 || !!imgs.hasNextPage)
  const cover =
    (meta.data.cover_key && rows.find((r) => r.key === meta.data.cover_key)) || rows[0] || null
  const coverIndex = cover ? rows.findIndex((r) => r.key === cover.key) : 0

  return (
    <div className="flex min-h-[60vh] flex-col">
      <PublicAlbumHero
        meta={meta.data}
        cover={cover}
        coverIndex={coverIndex}
        loading={imgs.isLoading}
        onEnterImmersive={openImmersive}
      />
      {meta.data.description?.trim() && (
        <p className="mt-[-1rem] mb-5 max-w-[52rem] text-[13.5px] leading-relaxed text-muted">
          {meta.data.description}
        </p>
      )}

      {rows.length > 0 && (
        <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
          <div className="font-mono text-[10px] tracking-[0.14em] text-muted">
            {t('albums.scrollGallery').toUpperCase()}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Segmented<AlbumPublicMode>
              compact
              options={[
                { value: 'gallery', label: t('albums.modeGallery') },
                { value: 'immersive', label: t('albums.modeImmersive') },
              ]}
              value={mode}
              onChange={(m) => {
                if (m === 'immersive') openImmersive(activeIndex ?? 0)
                else closeImmersive()
              }}
            />
          </div>
        </div>
      )}

      <PublicAlbumMasonry
        rows={rows}
        loading={imgs.isLoading}
        hasNextPage={!!imgs.hasNextPage}
        isFetchingNextPage={imgs.isFetchingNextPage}
        onFetchMore={() => {
          void imgs.fetchNextPage()
        }}
        onOpenImmersive={openImmersive}
        clickToImmersive={meta.data.click_to_immersive !== false}
      />

      {immersive && activeIndex != null && rows[activeIndex] && (
        <AlbumImmersive
          items={rows}
          index={activeIndex}
          totalCount={meta.data.image_count}
          canPrev={!!canPrev}
          canNext={!!canNext}
          onClose={closeImmersive}
          onPrev={goPrev}
          onNext={goNext}
          onSelectIndex={selectIndex}
        />
      )}

      {activeIndex != null && !rows[activeIndex] && imgs.isFetchingNextPage && (
        <div
          className="fixed inset-0 z-[90] flex items-center justify-center bg-black/90 text-sm text-white/70"
          data-testid="album-immersive-loading"
        >
          {t('discover.loading')}
        </div>
      )}

      <ShareBrandFooter
        siteName={(cfg.data?.site_name || '').trim()}
        branding={cfg.data?.share_branding || 'off'}
        helpURL={cfg.data?.help_url}
        upgradeURL={cfg.data?.upgrade_url}
        className="mt-auto pt-10 pb-2"
        testId="album-share-brand-foot"
      />
    </div>
  )
}
