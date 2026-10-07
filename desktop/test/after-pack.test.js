'use strict'

// after-pack.js decides whether electron-builder is about to sign the packed
// macOS app with an identity, which it only does after the hook returns. When
// it will, the hook must leave the bundle alone apart from the marker the app
// reads to update in place; when it will not, the hook must ad-hoc sign it or
// an Apple Silicon Mac refuses to open it. The decision is a pure function of
// the environment and what the keychain lookup found, so every case is checked
// here without a keychain or a Mac.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { identitySearch, planMacSigning } = require('../scripts/after-pack')

const DEVELOPER_ID = { name: 'Developer ID Application: Example Person (TEAM123456)' }
const IDENTITY_SIGNED = { adHoc: false, marker: DEVELOPER_ID.name }
const AD_HOC = { adHoc: true, marker: null }

function plan (env, identity, configIdentity = undefined, platform = 'darwin') {
  return planMacSigning({ platform, env, configIdentity, identity })
}

describe('planMacSigning', () => {
  test('the release secrets and the identity they hold: electron-builder signs, the marker names it', () => {
    assert.deepEqual(plan({ CSC_LINK: 'base64', CSC_KEY_PASSWORD: 'secret' }, DEVELOPER_ID), IDENTITY_SIGNED)
  })

  test('no secrets and discovery off: ad-hoc, no marker, even with an identity in the keychain', () => {
    assert.deepEqual(plan({ CSC_IDENTITY_AUTO_DISCOVERY: 'false' }, DEVELOPER_ID), AD_HOC)
  })

  test('discovery on and an identity found: electron-builder signs, the marker names it', () => {
    assert.deepEqual(plan({}, DEVELOPER_ID), IDENTITY_SIGNED)
  })

  test('discovery on and no identity found: ad-hoc, no marker', () => {
    assert.deepEqual(plan({}, null), AD_HOC)
  })

  test('the secrets with an identity the keychain does not hold: ad-hoc, no marker', () => {
    assert.deepEqual(plan({ CSC_LINK: 'base64', CSC_KEY_PASSWORD: 'secret' }, null), AD_HOC)
  })

  test('identity set to null in the mac block turns electron-builder signing off', () => {
    assert.deepEqual(plan({ CSC_LINK: 'base64' }, DEVELOPER_ID, null), AD_HOC)
  })

  test('a named identity searches even with discovery off', () => {
    assert.deepEqual(plan({ CSC_NAME: 'Example Person (TEAM123456)', CSC_IDENTITY_AUTO_DISCOVERY: 'false' }, DEVELOPER_ID), IDENTITY_SIGNED)
  })

  test('a pull-request build is never signed by electron-builder unless CSC_FOR_PULL_REQUEST says so', () => {
    assert.deepEqual(plan({ GITHUB_BASE_REF: 'main' }, DEVELOPER_ID), AD_HOC)
    assert.deepEqual(plan({ GITHUB_BASE_REF: 'main', CSC_FOR_PULL_REQUEST: 'true' }, DEVELOPER_ID), IDENTITY_SIGNED)
    // GitHub sets GITHUB_BASE_REF to an empty string outside pull requests.
    assert.deepEqual(plan({ GITHUB_BASE_REF: '' }, DEVELOPER_ID), IDENTITY_SIGNED)
  })

  test('off macOS electron-builder signs nothing', () => {
    assert.deepEqual(plan({}, DEVELOPER_ID, undefined, 'linux'), AD_HOC)
  })
})

describe('identitySearch', () => {
  test('no qualifier with discovery on: search for any identity', () => {
    assert.deepEqual(identitySearch({ platform: 'darwin', env: {}, configIdentity: undefined }), { qualifier: null })
  })

  test('the mac block identity wins over CSC_NAME, the way findIdentity reads them', () => {
    assert.deepEqual(identitySearch({ platform: 'darwin', env: { CSC_NAME: 'Other' }, configIdentity: 'Configured' }), { qualifier: 'Configured' })
    assert.deepEqual(identitySearch({ platform: 'darwin', env: { CSC_NAME: ' Named ' }, configIdentity: undefined }), { qualifier: 'Named' })
  })

  test('nothing to search for when electron-builder would not look', () => {
    assert.equal(identitySearch({ platform: 'darwin', env: { CSC_IDENTITY_AUTO_DISCOVERY: 'false' }, configIdentity: undefined }), null)
    assert.equal(identitySearch({ platform: 'darwin', env: {}, configIdentity: null }), null)
  })
})
