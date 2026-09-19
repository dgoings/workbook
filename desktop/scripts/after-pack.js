'use strict'

// Put the right Workbook CLI into the packed app, then ad-hoc sign the packed
// macOS app before the DMG and ZIP are built from it.
//
// electron-builder is configured with `identity: null`, which skips signing
// entirely — but macOS on Apple Silicon refuses to launch an arm64 bundle whose
// signature repackaging invalidated, and Electron's own signature is
// invalidated the moment the bundle is renamed and given extra resources. The
// result is a DMG that mounts, installs, and then fails to open.
//
// This has to run here rather than after `electron-builder` finishes: by then
// the DMG and ZIP have already been built from the unsigned bundle, and signing
// the leftover .app in dist/ fixes nothing that was shipped.

const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const { Arch } = require('electron-builder')

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

exports.default = async function afterPack (context) {
  placeWorkbook(context)

  if (context.electronPlatformName !== 'darwin') return

  const app = path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`)
  console.log(`  • ad-hoc signing  ${path.basename(app)}`)

  execFileSync('codesign', ['--force', '--deep', '--sign', '-', app], { stdio: 'inherit' })
  execFileSync('codesign', ['--verify', '--deep', '--strict', app], { stdio: 'inherit' })
}
