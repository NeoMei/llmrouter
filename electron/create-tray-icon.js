// Regenerate tray assets from the committed LLMRouter artwork.
// Run from electron/: npx electron create-tray-icon.js
const fs = require('node:fs');
const path = require('node:path');
const { app, nativeImage } = require('electron');

if (!app || !nativeImage) {
  throw new Error('Run this script with Electron: npx electron create-tray-icon.js');
}

app.whenReady().then(() => {
  const source = nativeImage.createFromPath(path.join(__dirname, '../web/public/logo.png'));
  if (source.isEmpty()) throw new Error('LLMRouter source icon is missing');
  fs.writeFileSync(path.join(__dirname, 'tray-icon-windows.png'), source.resize({ width: 32, height: 32 }).toPNG());
  for (const size of [22, 44]) {
    const image = source.resize({ width: size, height: size });
    const pixels = image.toBitmap();
    // Electron bitmaps are BGRA: preserve alpha and make the artwork black.
    for (let offset = 0; offset < pixels.length; offset += 4) {
      pixels[offset] = 0;
      pixels[offset + 1] = 0;
      pixels[offset + 2] = 0;
    }
    const template = nativeImage.createFromBitmap(pixels, { width: size, height: size });
    const filename = size === 22 ? 'tray-iconTemplate.png' : 'tray-iconTemplate@2x.png';
    fs.writeFileSync(path.join(__dirname, filename), template.toPNG());
  }
  app.quit();
}).catch((error) => {
  console.error(error.message);
  app.exit(1);
});
