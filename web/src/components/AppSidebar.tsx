import { useEffect, useState } from 'react'
import { Link, NavLink } from 'react-router'
import logoUrl from '../assets/cubedriver-logo.svg'
import { listPxeImages, stripBootMode } from '../api/deploy'
import { SettingsModal } from './SettingsModal'

type NavItem = { to: string; label: string; end?: boolean }

// Provisioning group (Hardware, Clusters) and the Enterprise group are shown
// as separate sections split by a hairline divider.
const provisioningNav: NavItem[] = [
  { to: '/hardware', label: 'Hardware' },
  { to: '/', label: 'Clusters', end: true },
]
const enterpriseNav: NavItem[] = [
  { to: '/enterprise', label: 'Enterprise Modules' },
]

const navLinkClass = ({ isActive }: { isActive: boolean }): string =>
  `secondary-body2 flex items-center px-[22px] py-[7px] font-medium transition ${
    isActive
      ? 'bg-functional-hover-secondary text-primary'
      : 'text-functional-text hover:bg-functional-hover-grey'
  }`

// One nav section (list of links).
const NavList = ({ items }: { items: NavItem[] }) => (
  <ul className="flex flex-col py-4">
    {items.map((item) => (
      <li key={item.to}>
        <NavLink to={item.to} end={item.end} className={navLinkClass}>
          {item.label}
        </NavLink>
      </li>
    ))}
  </ul>
)

// cubeDriver version shown in the nav. Bump on release (wire to build later).
const VERSION = '2.0.0'

// compactImageName fits a long PXE image name into the narrow nav pill:
// middle-truncate the boot-mode-stripped name, so both the version prefix and
// the commit suffix — the parts that tell CUBE_<ver>_<ts>_<commit> builds apart
// — stay visible. The full (stripped) name is on the pill's tooltip.
export const compactImageName = (name: string): string => {
  const base = stripBootMode(name)
  if (base.length <= 20) return base
  return `${base.slice(0, 11)}…${base.slice(-8)}`
}

// Official cubeDriver logo (full wordmark, basic variant).
const CubeDriverLogo = () => (
  <img src={logoUrl} alt="cubeDriver" className="h-[26px]" />
)

// Left navigation panel in the cube-license-portal visual language: fixed 200px,
// grey-0 background with a soft shadow, then logo → version → menu → footer,
// each block separated by a hairline divider.
export const AppSidebar = () => {
  // Current default PXE image (what a node boots by default). Falls back to the
  // mode label when no PXE root is configured.
  const [defaultImage, setDefaultImage] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const loadDefaultImage = () => {
    listPxeImages()
      .then((imgs) => setDefaultImage(imgs.find((i) => i.default)?.name ?? ''))
      .catch(() => {})
  }
  useEffect(loadDefaultImage, [])

  return (
    <nav
      className="flex min-h-svh w-[200px] shrink-0 flex-col bg-grey-0"
      style={{ boxShadow: '0px 0px 2px 0px rgba(0, 0, 0, 0.20)' }}
    >
      <Link to="/" className="flex h-[54px] shrink-0 items-center px-[22px] py-[14px]">
        <CubeDriverLogo />
      </Link>
      <div className="h-px w-full bg-functional-border-divider" />

      <div className="flex flex-col gap-y-2 px-[22px] py-4">
        <span className="secondary-body2 font-medium text-functional-text">
          v{VERSION}
        </span>
        <div
          className="primary-body5 flex max-w-full items-center justify-center truncate rounded border border-primary px-[5px] font-medium text-primary"
          title={
            defaultImage
              ? `Default PXE image: ${stripBootMode(defaultImage)}`
              : undefined
          }
        >
          {defaultImage ? compactImageName(defaultImage) : 'Provisioning'}
        </div>
      </div>
      <div className="h-px w-full bg-functional-border-divider" />

      <NavList items={provisioningNav} />
      <div className="h-px w-full bg-functional-border-divider" />
      <NavList items={enterpriseNav} />
      <div className="h-px w-full bg-functional-border-divider" />
      <div className="flex flex-col py-4">
        <button
          type="button"
          onClick={() => setSettingsOpen(true)}
          className="secondary-body2 flex items-center px-[22px] py-[7px] font-medium text-functional-text transition hover:text-primary"
        >
          Settings
        </button>
      </div>

      <div className="flex w-full flex-1 flex-col justify-end">
        <div className="flex h-[21px] items-center px-[22px] pb-4 pt-2 text-[9px] text-functional-text-light">
          Copyright©Bigstack
        </div>
      </div>

      {settingsOpen && (
        <SettingsModal onClose={() => setSettingsOpen(false)} />
      )}
    </nav>
  )
}
