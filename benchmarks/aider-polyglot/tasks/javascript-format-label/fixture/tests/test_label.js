const assert = require('assert');
const { formatLabel } = require('../src/label');
assert.equal(formatLabel('  hello world  '), 'Hello World');
