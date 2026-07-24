import { Route, Routes } from 'react-router'
import { AppHeader } from './components/AppHeader'
import { ClusterPage } from './pages/cluster/ClusterPage'
import { LandingPage } from './pages/landing/LandingPage'

export const App = () => {
  return (
    <div className="flex h-full flex-col bg-scene-background">
      <AppHeader />
      <main className="flex-1 overflow-y-auto">
        <Routes>
          <Route path="/" element={<LandingPage />} />
          <Route path="/clusters/:id" element={<ClusterPage />} />
        </Routes>
      </main>
    </div>
  )
}
