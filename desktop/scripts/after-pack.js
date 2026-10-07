'use strict'

// Put the right Workbook CLI into the packed app, then settle how the packed
// macOS app is signed before the DMG and ZIP are built from it.
//
// electron-builder runs this hook first and signs afterwards. When it has a
// Developer ID identity (a release, where the workflow hands it CSC_LINK, or a
// developer's Mac with one in the login keychain) it signs the whole bundle
// itself, with the hardened runtime notarization requires, and this hook only
// marks the bundle as identity-signed so the app knows Squirrel.Mac can update
// it in place.
//
// When it has none, electron-builder skips signing altogether — but macOS on
// Apple Silicon refuses to launch an arm64 bundle whose signature repackaging
// invalidated, and Electron's own signature is invalidated the moment the
// bundle is renamed and given extra resources. The result would be a DMG that
// mounts, installs, and then fails to open. So in that case this hook ad-hoc
// signs the bundle, and it has to do so here rather than after
// `electron-builder` finishes: by then the DMG and ZIP have already been built
// from the unsigned bundle, and signing the leftover .app in dist/ fixes
// nothing that was shipped.

const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const { Arch } = require('electron-builder')
// The app reads the marker by the same name, so it is spelled in one place.
const { SIGNED_MARKER } = require('../src/main/updateflow')

const GOOS = { darwin: 'darwin', linux: 'linux', win32: 'windows' }
const GOARCH = { [Arch.x64]: 'amd64', [Arch.arm64]: 'arm64' }

// The CLI the app bundles has to match the bundle's own platform and
// architecture, which electron-builder's own extraResources cannot express: it
// copies one file into every bundle, so a two-architecture build shipped the
// host's binary in both. A release stages one binary per target under
// build/<goos>-<goarch>; a local `npm run dist` stages one for the host under
// build/, which is the fallback. Copying happens here, before signing, because
// the resources are part of what gets signed.
function placeWorkbook (context) {
  const goos = GOOS[context.electronPlatformName]
  const goarch = GOARCH[context.arch]
  // Arch is a numeric enum, and `linux/2` names nothing a reader can act on.
  if (!goos || !goarch) throw new Error(`no CLI target for ${context.electronPlatformName}/${Arch[context.arch] ?? context.arch}`)
  const binary = goos === 'windows' ? 'workbook.exe' : 'workbook'
  const build = path.join(__dirname, '..', 'build')
  const candidates = [path.join(build, `${goos}-${goarch}`), build]
  const source = candidates.find((directory) => fs.existsSync(path.join(directory, binary)))
  if (!source) throw new Error(`no staged Workbook CLI at ${candidates.join(' or ')}; run npm run stage`)
  const resources = context.packager.getResourcesDir(context.appOutDir)
  fs.mkdirSync(resources, { recursive: true })
  fs.copyFileSync(path.join(source, binary), path.join(resources, binary))
  // copyFileSync carries the source's mode across, but a mode that lost the
  // executable bit anywhere upstream (an artifact download, say) would ship an
  // app whose CLI cannot be run at all.
  fs.chmodSync(path.join(resources, binary), 0o755)
  // The MIT license travels with the binary: the app redistributes it.
  fs.copyFileSync(path.join(source, 'WORKBOOK-LICENSE'), path.join(resources, 'WORKBOOK-LICENSE'))
  console.log(`  • bundling ${binary} from ${path.relative(process.cwd(), source)}`)
}

// The decision below mirrors electron-builder 26's own, which it takes only
// after this hook has returned, so it cannot be read back from the packager.
// Each step names the code it follows, in app-builder-lib/out:
//
//   macPackager.js `sign`: isSignAllowed(), then `identity === null` skips
//   signing, then findSigningIdentity; no identity found means no signing.
//   codeSign/macCodeSign.js `isSignAllowed`: never off macOS, and never in a
//   pull-request build unless CSC_FOR_PULL_REQUEST says otherwise.
//   codeSign/macCodeSign.js `findIdentity`: the configured identity or CSC_NAME
//   narrows the search; with neither, CSC_IDENTITY_AUTO_DISCOVERY=false means
//   no search at all.
//
// CSC_LINK decides which keychain is searched, not whether there is a search:
// electron-builder imports it into a temporary keychain and looks there.
//
// Three configurations take electron-builder down paths this does not follow,
// and none is set in package.json: `identity: "-"` (electron-builder signs
// ad-hoc itself, with the hardened runtime), `type: "development"` (it looks
// for development certificates instead), and a custom `mac.sign` function
// (it hands signing to that). Setting any of them means revisiting this.

// builder-util's isPullRequest: each CI's variable, set to anything but "false".
const PULL_REQUEST_VARIABLES = ['TRAVIS_PULL_REQUEST', 'CIRCLE_PULL_REQUEST', 'BITRISE_PULL_REQUEST', 'APPVEYOR_PULL_REQUEST_NUMBER', 'GITHUB_BASE_REF']

// builder-util's isEnvTrue, which counts a variable set to nothing as true.
function envTrue (value) {
  if (value == null) return false
  const trimmed = value.trim()
  return trimmed === 'true' || trimmed === '' || trimmed === '1'
}

/**
 * What electron-builder will search the keychain for, or null when it will
 * not sign with an identity at all and so never searches.
 *
 * @param {{ platform: string, env: Record<string, string|undefined>,
 *           configIdentity: string|null|undefined }} options
 *   configIdentity is the mac block's `identity`: undefined when unset, null
 *   when signing is turned off.
 * @returns {{ qualifier: string|null } | null}
 */
function identitySearch ({ platform, env, configIdentity }) {
  if (platform !== 'darwin') return null
  const pullRequest = PULL_REQUEST_VARIABLES.some((name) => env[name] && env[name] !== 'false')
  if (pullRequest && !envTrue(env.CSC_FOR_PULL_REQUEST)) return null
  if (configIdentity === null) return null
  const qualifier = (configIdentity || env.CSC_NAME || '').trim()
  if (qualifier) return { qualifier }
  if (env.CSC_IDENTITY_AUTO_DISCOVERY === 'false') return null
  return { qualifier: null }
}

/**
 * How the packed macOS app gets signed: by electron-builder with `identity`,
 * leaving this hook to write the marker, or ad-hoc by this hook.
 *
 * Pure, so it can be tested without a keychain: `identity` is what
 * electron-builder's own lookup found for identitySearch's answer, or null.
 *
 * @param {{ platform: string, env: Record<string, string|undefined>,
 *           configIdentity: string|null|undefined,
 *           identity: { name: string }|null }} options
 * @returns {{ adHoc: boolean, marker: string|null }}
 */
function planMacSigning ({ platform, env, configIdentity, identity }) {
  if (!identitySearch({ platform, env, configIdentity }) || !identity) return { adHoc: true, marker: null }
  return { adHoc: false, marker: identity.name }
}

// The identity electron-builder will sign with, found the way it finds it: the
// same keychain (the packager's memoized codeSigningInfo, which imports
// CSC_LINK once for both of us) and the same certificate types in the same
// order as MacTargetHelper.findSigningIdentity, through electron-builder's own
// findIdentity. Required lazily because only a macOS pack needs it.
async function findSigningIdentity (packager, search) {
  const { findIdentity } = require('app-builder-lib/out/codeSign/macCodeSign')
  const { keychainFile } = await packager.codeSigningInfo.value
  const config = packager.platformSpecificBuildOptions
  const identity = await findIdentity('Developer ID Application', search.qualifier, keychainFile)
  if (identity || config.type === 'distribution') return identity
  return findIdentity('Mac Developer', search.qualifier, keychainFile)
}

async function signMac (context) {
  const app = path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`)
  const marker = path.join(context.packager.getResourcesDir(context.appOutDir), SIGNED_MARKER)
  const options = {
    platform: process.platform,
    env: process.env,
    configIdentity: context.packager.platformSpecificBuildOptions.identity
  }
  const search = identitySearch(options)
  const identity = search ? await findSigningIdentity(context.packager, search) : null
  const plan = planMacSigning({ ...options, identity })

  if (!plan.adHoc) {
    // The app updates in place only when this file is in its Resources. Its
    // contents, the identity's name, are for a person looking inside the
    // bundle; the app reads nothing but its presence. Written before
    // electron-builder signs, so the signature covers it.
    fs.writeFileSync(marker, `${plan.marker}\n`)
    console.log(`  • electron-builder will sign ${path.basename(app)} as ${plan.marker}`)
    return
  }

  // An appOutDir reused from an identity-signed run must not keep claiming it.
  fs.rmSync(marker, { force: true })
  console.log(`  • ad-hoc signing  ${path.basename(app)}`)
  // No --options runtime: the hardened runtime enforces library validation,
  // which an ad-hoc signature (no team) can never satisfy.
  execFileSync('codesign', ['--force', '--deep', '--sign', '-', app], { stdio: 'inherit' })
  execFileSync('codesign', ['--verify', '--deep', '--strict', app], { stdio: 'inherit' })
}

exports.default = async function afterPack (context) {
  placeWorkbook(context)
  if (context.electronPlatformName === 'darwin') await signMac(context)
}

exports.identitySearch = identitySearch
exports.planMacSigning = planMacSigning
