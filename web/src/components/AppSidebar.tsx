import { Link, NavLink } from 'react-router'

type NavItem = { to: string; label: string; end?: boolean }

const navItems: NavItem[] = [
  { to: '/', label: 'Clusters', end: true },
  { to: '/hardware', label: 'Hardware' },
]

// cubeDriver version shown in the nav. Bump on release (wire to build later).
const VERSION = '2.0.0'

// CubeDriverLogo — follows the cube-license-portal mark: the isometric cubeCOS
// cube in the portal blues, a small badge (here a deploy/drive triangle instead
// of the license seal), and a two-tone "cubeDriver" wordmark.
const CubeDriverLogo = () => (
  <svg viewBox="0 0 132 26" fill="none" xmlns="http://www.w3.org/2000/svg" className="h-[26px]">
    <path
      d="M1.27303 5.81678L11.0575 0.103291C11.2981 -0.0344304 11.597 -0.0344304 11.8376 0.103291L21.459 5.46264C21.9985 5.77349 22.0063 6.56441 21.4707 6.88314L11.6629 12.5809C11.4145 12.7304 11.104 12.7304 10.8556 12.5809L1.2575 7.23728C0.721899 6.91855 0.729661 6.1237 1.27303 5.81678Z"
      fill="#7A5AF8"
    />
    <path
      d="M23.093 18.5974L23.2056 9.87367C23.2521 9.21654 22.5613 8.77189 22.0063 9.10636L13.6229 13.9069C13.3784 14.0525 12.6565 14.5759 12.6565 15.4809V25.1805C12.6565 25.814 13.328 26.2075 13.8597 25.8848C13.8597 25.8848 22.0373 20.9858 22.2663 20.7537C22.4953 20.5215 23.093 20.1595 23.093 18.6013V18.5974Z"
      fill="#4C68F9"
    />
    <path
      d="M1.18376 20.7065C0.485147 20.3523 0.0659797 19.6834 0.0853856 18.9594L0 9.51953C0.0194059 8.90568 0.667561 8.53186 1.19152 8.83485L4.21884 10.5741C4.48276 10.7275 4.638 11.0187 4.62248 11.3296L4.53321 16.0908C4.49828 17.0234 4.71175 17.6687 5.17361 18.2314C5.41812 18.5305 5.71697 18.7784 6.04687 18.9712L10.4171 21.58C10.6616 21.7256 10.813 21.9971 10.813 22.2883V25.1726C10.813 25.7982 10.157 26.1917 9.62143 25.8848L1.176 20.7104L1.18376 20.7065Z"
      fill="#3B4FD9"
    />
    {/* deploy/drive badge: white ring + primary disc + forward triangle */}
    <circle cx="18.6" cy="20.2" r="5.6" fill="#FFFFFF" />
    <circle cx="18.6" cy="20.2" r="4.5" fill="#4C68F9" />
    <path d="M17.1 17.8L17.1 22.6L21.2 20.2Z" fill="#FFFFFF" />
    {/* wordmark */}
    <text
      x="29"
      y="18.8"
      fontFamily="Urbanist, Inter, sans-serif"
      fontSize="15.5"
      fontWeight="600"
      letterSpacing="-0.2"
    >
      <tspan fill="#14182B">cube</tspan>
      <tspan fill="#4C68F9">Driver</tspan>
    </text>
  </svg>
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
