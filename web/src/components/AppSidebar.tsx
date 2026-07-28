import { Link, NavLink } from 'react-router'
import logoUrl from '../assets/cubedriver-logo.svg'

type NavItem = { to: string; label: string; end?: boolean }

const navItems: NavItem[] = [
  { to: '/', label: 'Clusters', end: true },
  { to: '/hardware', label: 'Hardware' },
]

// cubeDriver version shown in the nav. Bump on release (wire to build later).
const VERSION = '2.0.0'

// Official cubeDriver logo (full wordmark, basic variant).
const CubeDriverLogo = () => (
  <img src={logoUrl} alt="cubeDriver" className="h-[26px]" />
)

// Left navigation panel in the cube-license-portal visual language: fixed 200px,
// grey-0 background with a soft shadow, then logo → version → menu → footer,
// each block separated by a hairline divider.
export const AppSidebar = () => {
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
        <div className="primary-body5 flex w-fit items-center justify-center rounded border border-primary px-[5px] font-medium text-primary">
          Provisioning
        </div>
      </div>
      <div className="h-px w-full bg-functional-border-divider" />

      <ul className="flex flex-col py-4">
        {navItems.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                `secondary-body2 flex items-center px-[22px] py-[7px] font-medium transition ${
                  isActive
                    ? 'bg-functional-hover-secondary text-primary'
                    : 'text-functional-text hover:bg-functional-hover-grey'
                }`
              }
            >
              {item.label}
            </NavLink>
          </li>
        ))}
      </ul>
      <div className="h-px w-full bg-functional-border-divider" />

      <div className="flex w-full flex-1 flex-col justify-end">
        <div className="flex h-[21px] items-center px-[22px] pb-4 pt-2 text-[9px] text-functional-text-light">
          Copyright©Bigstack
        </div>
      </div>
    </nav>
  )
}
