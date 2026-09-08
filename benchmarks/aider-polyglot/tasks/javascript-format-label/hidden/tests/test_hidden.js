const assert = require('assert');
const { formatLabel } = require(process.cwd() + '/src/label');
assert.equal(formatLabel("  o'connor   mcGEE "), "O'connor Mcgee");
