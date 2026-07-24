import { CosStroke } from '@cube-frontend/ui-library'
import { Link } from 'react-router'

export const AppHeader = () => {
  return (
    <header className="relative flex h-[54px] flex-row items-center gap-x-2.5 px-5">
      <Link to="/" className="flex items-center gap-x-2.5">
        <img src="/cube.png" alt="Cube" className="h-7 w-7" />
        <span className="primary-h4 text-functional-text-title">
          Cube Snapshot Generator
        </span>
      </Link>
      <div className="absolute bottom-0 left-0 w-full px-5">
        <CosStroke />
      </div>
    </header>
  )
}
