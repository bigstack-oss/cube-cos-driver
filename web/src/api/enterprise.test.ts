import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  Artifacts,
  Install,
  cancelInstall,
  getArtifacts,
  getInstall,
  nextStep,
  startInstall,
} from './enterprise'

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

const install: Install = {
  ClusterID: 'ID',
  Module: 'appfw',
  StartedAt: '2026-08-08T00:00:00Z',
  Manual: false,
  ManualStep: 0,
  SimulateAirgap: false,
  Params: {
    Project: 'p',
    PublicNet: 'pub',
    MgmtNet: 'mgmt',
    LBIP: '10.0.0.1',
    OSImage: 'os.img',
    Framework: 'fw.img',
    AppFile: 'app.tgz',
    FsImage: 'fs.img',
    LBImage: 'lb.img',
    StorageBackend: 'CubeStorage',
  },
  Steps: [{ Name: 'step1', Title: 'Step 1', State: 'pending', Output: '', Err: '' }],
  Current: 0,
  State: 'running',
  Portal: 'https://portal',
}

describe('enterprise API client', () => {
  it('startInstall POSTs to the install endpoint with PascalCase params', async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 202, json: async () => install })

    const body = {
      module: 'appfw' as const,
      params: install.Params,
      manual: false,
      simulateAirgap: false,
      password: 'secret',
      manifest: '',
    }
    await startInstall('ID', body)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/clusters/ID/enterprise/install')
    expect(init.method).toBe('POST')
    const parsed = JSON.parse(init.body as string)
    expect(parsed.module).toBe('appfw')
    expect(parsed.manual).toBe(false)
    expect(parsed.simulateAirgap).toBe(false)
    expect(parsed.password).toBe('secret')
    expect(parsed.params.OSImage).toBe('os.img')
    expect(parsed.params.Project).toBe('p')
    expect(parsed.params.LBIP).toBe('10.0.0.1')
  })

  it('getInstall GETs with the module query param', async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 200, json: async () => install })

    const result = await getInstall('ID', 'cmp')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/v1/clusters/ID/enterprise/install?module=cmp')
    expect(init?.method === undefined || init.method === 'GET').toBe(true)
    expect(result).toEqual(install)
  })

  it('nextStep POSTs to the step/next endpoint', async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 200, json: async () => install })

    await nextStep('ID', 'appfw')

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/v1/clusters/ID/enterprise/install/step/next?module=appfw')
    expect(init.method).toBe('POST')
  })

  it('cancelInstall POSTs to the cancel endpoint', async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    await cancelInstall('ID', 'cmp')

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/v1/clusters/ID/enterprise/install/cancel?module=cmp')
    expect(init.method).toBe('POST')
  })

  it('getArtifacts hits the artifacts endpoint and returns parsed body', async () => {
    const artifacts: Artifacts = { AppFW: ['a1'], CMP: ['c1'] }
    fetchMock.mockResolvedValue({ ok: true, status: 200, json: async () => artifacts })

    const result = await getArtifacts()

    const [url] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/enterprise/artifacts')
    expect(result).toEqual(artifacts)
  })

  it('rejects with the server message on a non-ok response', async () => {
    fetchMock.mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({ message: 'unknown module' }),
    })

    await expect(getArtifacts()).rejects.toThrow('unknown module')
  })
})
