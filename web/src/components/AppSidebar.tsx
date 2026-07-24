import { Link, NavLink } from 'react-router'

type NavItem = { to: string; label: string; end?: boolean }

const navItems: NavItem[] = [
  { to: '/', label: 'Clusters', end: true },
  { to: '/hardware', label: 'Hardware' },
]

// Left navigation panel in the CubeCOS visual language: fixed 200px,
// grey-0 background with a soft shadow, logo/title block on top, vertical
// nav links below.
export const AppSidebar = () => {
  return (
    <nav
      className="flex min-h-svh w-[200px] shrink-0 flex-col bg-grey-0"
      style={{ boxShadow: '0px 0px 2px 0px rgba(0, 0, 0, 0.20)' }}
    >
      <Link
        to="/"
        className="flex h-[54px] shrink-0 items-center gap-x-2.5 px-[22px]"
      >
        <img src="/cube.png" alt="Cube" className="h-6 w-6" />
        <span className="primary-body2 font-semibold text-functional-text-title">
          Snapshot Generator
        </span>
      </Link>
      <div className="h-px w-full bg-functional-border-divider" />
      <ul className="flex flex-col py-2">
        {navItems.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                `primary-body3 flex h-10 items-center px-[22px] ${
                  isActive
                    ? 'bg-primary-0 font-semibold text-primary'
                    : 'text-functional-text hover:bg-grey-50'
                }`
              }
            >
              {item.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  )
}
