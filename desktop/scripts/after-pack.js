'use strict'

// Ad-hoc sign the packed macOS app before the DMG and ZIP are built from it.
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
const path = require('node:path')

exports.default = async function afterPack (context) {
  if (context.electronPlatformName !== 'darwin') return

  const app = path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`)
  console.log(`  • ad-hoc signing  ${path.basename(app)}`)

  execFileSync('codesign', ['--force', '--deep', '--sign', '-', app], { stdio: 'inherit' })
  execFileSync('codesign', ['--verify', '--deep', '--strict', app], { stdio: 'inherit' })
}
