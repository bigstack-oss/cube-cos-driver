import { useParams } from 'react-router'

export const ClusterPage = () => {
  const { id } = useParams()
  return (
    <div className="p-8">
      <h1 className="primary-h2">Cluster {id}</h1>
    </div>
  )
}
