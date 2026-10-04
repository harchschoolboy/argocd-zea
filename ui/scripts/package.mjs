// Packs dist/resources/zea/extension-zea.js into the layout expected by
// argocd-extension-installer: a tar with a top-level "resources" directory.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const uiDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const distDir = join(uiDir, 'dist');
const jsFile = join(distDir, 'resources', 'zea', 'extension-zea.js');
const tarName = 'extension-zea.tar.gz';

if (!existsSync(jsFile)) {
  console.error(`Missing ${jsFile}. Run "npm run build" first.`);
  process.exit(1);
}

execFileSync('tar', ['-czf', tarName, 'resources'], { cwd: distDir, stdio: 'inherit' });

const sha = createHash('sha256').update(readFileSync(join(distDir, tarName))).digest('hex');
writeFileSync(join(distDir, 'extension-zea_checksums.txt'), `${sha}  ${tarName}\n`);

console.log(`Packed dist/${tarName} (sha256 ${sha})`);
