#!/usr/bin/env node
'use strict'

// Build assets/icon.ico from assets/icon.png, for the Windows installer.
//
// Written by hand rather than pulled from a package: an .ico is a 6-byte
// header, a 16-byte directory entry per size, and the image payloads, and
// every size Windows asks for is already a PNG that sips can produce. A
// dependency to concatenate three buffers is not worth the supply chain.

const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const SIZES = [16, 24, 32, 48, 64, 128, 256]

const assets = path.join(__dirname, '..', 'assets')
const source = path.join(assets, 'icon.png')
const target = path.join(assets, 'icon.ico')

if (!fs.existsSync(source)) {
  console.error(`make-ico: ${source} not found`)
  process.exit(1)
}

const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'workbench-ico-'))
try {
  const images = SIZES.map((size) => {
    const file = path.join(scratch, `${size}.png`)
    execFileSync('sips', ['-z', String(size), String(size), source, '--out', file],
      { stdio: 'ignore' })
    return { size, data: fs.readFileSync(file) }
  })

  const header = Buffer.alloc(6)
  header.writeUInt16LE(0, 0)              // reserved
  header.writeUInt16LE(1, 2)              // type: icon
  header.writeUInt16LE(images.length, 4)  // image count

  const directory = Buffer.alloc(16 * images.length)
  let offset = header.length + directory.length

  images.forEach((image, index) => {
    const entry = index * 16
    // 256 is written as 0: the field is one byte and 256 does not fit.
    directory.writeUInt8(image.size === 256 ? 0 : image.size, entry)
    directory.writeUInt8(image.size === 256 ? 0 : image.size, entry + 1)
    directory.writeUInt8(0, entry + 2)          // palette size (none)
    directory.writeUInt8(0, entry + 3)          // reserved
    directory.writeUInt16LE(1, entry + 4)       // colour planes
    directory.writeUInt16LE(32, entry + 6)      // bits per pixel
    directory.writeUInt32LE(image.data.length, entry + 8)
    directory.writeUInt32LE(offset, entry + 12)
    offset += image.data.length
  })

  fs.writeFileSync(target,
    Buffer.concat([header, directory, ...images.map((image) => image.data)]))
  console.log(`make-ico: ${images.length} sizes -> ${path.relative(process.cwd(), target)} ` +
    `(${(fs.statSync(target).size / 1024).toFixed(0)} KB)`)
} finally {
  fs.rmSync(scratch, { recursive: true, force: true })
}
