// Capture golden YAML outputs from the legacy generator.
// usage: cd /root/cube-snapshot-generator && node /root/cube-cos-snapshot/tools/gen-golden.mjs
import fs from 'fs';
import path from 'path';
import { createRequire } from 'module';

const LEGACY = process.cwd();
const require = createRequire(path.join(LEGACY, 'noop.js'));
const { getCubsysYaml, getNetworkYaml, getTimeYaml } = require('./server/routers/getYaml.js');
const { getControlInfo } = require('./server/routers/utils.js');

const SRC = '/root/cube-cos-snapshot/internal/generator/testdata';
for (const f of fs.readdirSync(path.join(SRC, 'fixtures'))) {
  const d = JSON.parse(fs.readFileSync(path.join(SRC, 'fixtures', f)));
  const ctl = getControlInfo(d.nodeData);
  for (const node of d.nodeData) {
    const out = path.join(SRC, 'golden', path.parse(f).name, node.hostname);
    fs.mkdirSync(out, { recursive: true });
    fs.writeFileSync(path.join(out, 'cubesys1_0.yml'), getCubsysYaml(node, d.clusterConfig, ctl));
    fs.writeFileSync(path.join(out, 'network1_0.yml'), getNetworkYaml(node, d.clusterConfig));
    fs.writeFileSync(path.join(out, 'time1_0.yml'), getTimeYaml(d.clusterConfig));
  }
}
console.log('golden files written');
