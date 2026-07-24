import { CosStroke } from '@cube-frontend/ui-library'
import { Link, NavLink } from 'react-router'

export const AppHeader = () => {
  return (
    <header className="relative flex h-[54px] flex-row items-center gap-x-6 px-5">
      <Link to="/" className="flex items-center gap-x-2.5">
        <img src="/cube.png" alt="Cube" className="h-7 w-7" />
        <span className="primary-h4 text-functional-text-title">
          Cube Snapshot Generator
        </span>
      </Link>
      <nav className="flex items-center gap-x-4">
        <NavLink
          to="/"
          end
          className={({ isActive }) =>
            `primary-body3 ${isActive ? 'font-semibold text-primary' : 'text-functional-text-light'}`
          }
        >
          Clusters
        </NavLink>
        <NavLink
          to="/hardware"
          className={({ isActive }) =>
            `primary-body3 ${isActive ? 'font-semibold text-primary' : 'text-functional-text-light'}`
          }
        >
          Hardware
        </NavLink>
      </nav>
      <div className="absolute bottom-0 left-0 w-full px-5">
        <CosStroke />
      </div>
    </header>
  )
}
