import { Route, Routes } from 'react-router'
import { AppSidebar } from './components/AppSidebar'
import { ClusterPage } from './pages/cluster/ClusterPage'
import { HardwarePage } from './pages/hardware/HardwarePage'
import { LandingPage } from './pages/landing/LandingPage'

export const App = () => {
  return (
    <div className="flex h-svh flex-row overflow-hidden bg-scene-background">
      <AppSidebar />
      <main className="min-w-0 flex-1 overflow-y-auto">
        <Routes>
          <Route path="/" element={<LandingPage />} />
          <Route path="/clusters/:id" element={<ClusterPage />} />
          <Route path="/hardware" element={<HardwarePage />} />
        </Routes>
      </main>
    </div>
  )
}
