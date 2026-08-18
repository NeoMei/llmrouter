const assert = require('node:assert/strict');
const test = require('node:test');

const packageJson = require('../package.json');

test('Linux deb package declares a maintainer email', () => {
  const maintainer = packageJson.build?.linux?.maintainer;

  assert.match(maintainer ?? '', /^[^<>\s]+\s+<[^<>\s]+@[^<>\s]+>$/);
});
