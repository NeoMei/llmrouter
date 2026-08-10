const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const { isAllowedNavigation } = require('../navigation-policy');

test('allows only credential-free navigation on the exact local origin', () => {
  const allowedOrigin = 'http://127.0.0.1:3000';

  assert.equal(isAllowedNavigation(`${allowedOrigin}/console?tab=files#latest`, allowedOrigin), true);
  assert.equal(isAllowedNavigation('http://127.0.0.1:3000@evil.example/', allowedOrigin), false);
  assert.equal(isAllowedNavigation('http://user:password@127.0.0.1:3000/', allowedOrigin), false);
  assert.equal(isAllowedNavigation('http://127.0.0.1:3001/', allowedOrigin), false);
  assert.equal(isAllowedNavigation('https://127.0.0.1:3000/', allowedOrigin), false);
  assert.equal(isAllowedNavigation('not a URL', allowedOrigin), false);
});

test('packages the navigation policy with the Electron application', () => {
  const packageJson = JSON.parse(
    fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8')
  );

  assert.ok(packageJson.build.files.includes('navigation-policy.js'));
});

test('desktop launcher binds the backend to loopback', () => {
  const mainSource = fs.readFileSync(path.join(__dirname, '..', 'main.js'), 'utf8');

  assert.match(mainSource, /SERVER_BIND_ADDRESS:\s*'127\.0\.0\.1'/);
});
