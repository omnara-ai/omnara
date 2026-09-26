import { describe, expect, it } from 'vitest'

import { createOmnaraClient } from './client'
import * as sdk from './generated/sdk.gen'

const orgID = 'org_aaaaaaaaaaaaaaaaaaaaaaaaaa'
const skillID = 'skl_aaaaaaaaaaaaaaaaaaaaaaaaaa'
const projectID = 'proj_aaaaaaaaaaaaaaaaaaaaaaaaaa'

function capturingClient() {
  const requests: Request[] = []
  const client = createOmnaraClient({
    baseUrl: 'https://api.example.test/v1',
    fetch: async (input, init) => {
      requests.push(new Request(input, init))
      return new Response(JSON.stringify({ error: 'stop', code: 'invalid_request' }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      })
    },
  })
  const sentForm = async () => {
    const [request] = requests
    if (!request) throw new Error('no request was sent')
    return request.formData()
  }
  return { client, sentForm }
}

describe('multipart request bodies', () => {
  it('sends createSkill owner as an application/json part', async () => {
    const { client, sentForm } = capturingClient()
    const archive = new File([new Uint8Array([0x1f, 0x8b])], 'skill.tar.gz')

    await expect(
      sdk.createSkill({
        client,
        path: { orgID },
        body: { owner: { kind: 'project', project_id: projectID }, archive },
      }),
    ).rejects.toThrow()

    const form = await sentForm()
    const owner = form.get('owner')
    expect(owner).toBeInstanceOf(Blob)
    expect((owner as Blob).type).toBe('application/json')
    expect(JSON.parse(await (owner as Blob).text())).toEqual({ kind: 'project', project_id: projectID })
    expect((form.get('archive') as File).name).toBe('skill.tar.gz')
  })

  it('keeps string fields as plain text parts', async () => {
    const { client, sentForm } = capturingClient()

    await expect(
      sdk.updateSkill({ client, path: { orgID, skillID }, body: { skill_md: 'replacement' } }),
    ).rejects.toThrow()

    expect((await sentForm()).get('skill_md')).toBe('replacement')
  })

  it('leaves a caller-supplied bodySerializer alone', async () => {
    const { client, sentForm } = capturingClient()
    const custom = new FormData()
    custom.append('owner', 'custom')

    await expect(
      sdk.createSkill({
        client,
        path: { orgID },
        body: { owner: { kind: 'org' }, archive: new File(['x'], 'x.zip') },
        bodySerializer: () => custom,
      }),
    ).rejects.toThrow()

    expect((await sentForm()).get('owner')).toBe('custom')
  })
})
