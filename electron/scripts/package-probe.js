const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const target = path.resolve(process.argv[2] || 'dist');
const writeChecksums = process.argv.includes('--write-checksums');

function walk(directory) {
  const results = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory()) results.push(...walk(fullPath));
    else results.push(fullPath);
  }
  return results;
}

assert(fs.existsSync(target), `Package output does not exist: ${target}`);
const files = fs.statSync(target).isDirectory() ? walk(target) : [target];
assert(files.length > 0, `Package output is empty: ${target}`);

const normalized = files.map((file) => file.replaceAll(path.sep, '/'));
const comparable = normalized.map((file) => file.toLowerCase());
assert(
  comparable.some((file) => file.endsWith('/resources/app.asar')),
  'Unpacked application is missing resources/app.asar'
);
assert(
  comparable.some((file) => /\/resources\/bin\/new-api(?:\.exe)?$/.test(file)),
  'Unpacked application is missing the backend binary'
);
assert(
  comparable.some((file) => file.endsWith('/resources/licenses/license')),
  'Unpacked application is missing the project license'
);

const artifacts = files.filter((file) =>
  /\.(?:AppImage|deb|dmg|exe|zip)$/i.test(file)
);
assert(artifacts.length > 0, 'No distributable package artifact was produced');

if (writeChecksums) {
  const lines = artifacts
    .sort()
    .map((file) => {
      const digest = crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
      return `${digest}  ${path.basename(file)}`;
    });
  fs.writeFileSync(path.join(target, 'SHA256SUMS'), `${lines.join('\n')}\n`);
}

console.log(`Package probe passed: ${artifacts.length} artifact(s), ${files.length} file(s)`);
