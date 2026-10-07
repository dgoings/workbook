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
const IDENTITY_SIGNED = { adHoc: false, marker: DEVELOPER_ID.name, refusal: null }
const AD_HOC = { adHoc: true, marker: null, refusal: null }
const SECRETS = { CSC_LINK: 'base64', CSC_KEY_PASSWORD: 'secret' }

// A refusal stops the build: neither electron-builder nor the hook signs, and
// no marker is written.
function assertRefused (result, cause) {
  assert.equal(result.adHoc, false)
  assert.equal(result.marker, null)
  assert.match(result.refusal ?? '', cause)
}

function plan (env, identity, configIdentity = undefined, platform = 'darwin') {
  return planMacSigning({ platform, env, configIdentity, identity })
}

describe('planMacSigning', () => {
  test('the release secrets and the identity they hold: electron-builder signs, the marker names it', () => {
    assert.deepEqual(plan(SECRETS, DEVELOPER_ID), IDENTITY_SIGNED)
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

  test('the secrets with no identity found for them: refused, naming expiry, revocation and the private key', () => {
    // An expired or revoked certificate, or a .p12 exported without its key,
    // leaves security find-identity -v with nothing. Shipping that release
    // ad-hoc would strand every signed install on the one before it.
    assertRefused(plan(SECRETS, null), /no signing identity was found.*expired.*revoked.*private key/)
  })

  test('the secrets with electron-builder signing turned off: refused, not ad-hoc', () => {
    assertRefused(plan(SECRETS, DEVELOPER_ID, null), /will not sign with it/)
    assertRefused(plan({ ...SECRETS, CSC_IDENTITY_AUTO_DISCOVERY: 'false' }, null), /will not sign with it/)
    assertRefused(plan({ ...SECRETS, GITHUB_BASE_REF: 'main' }, DEVELOPER_ID), /will not sign with it/)
  })

  test('an empty CSC_LINK is no certificate, so the build degrades rather than refusing', () => {
    assert.deepEqual(plan({ CSC_LINK: '  ' }, null), AD_HOC)
  })

  test('identity set to null in the mac block turns electron-builder signing off', () => {
    assert.deepEqual(plan({}, DEVELOPER_ID, null), AD_HOC)
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
