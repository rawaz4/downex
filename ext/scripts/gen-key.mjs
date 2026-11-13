// Generates the development signing key that pins the Chromium extension ID.
// - keys/dev-key.pem      private key (PKCS#8, gitignored; never commit)
// - keys/public-key.txt   base64 DER SubjectPublicKeyInfo, used as manifest "key"
// - EXTENSION_ID.txt      the resulting Chromium extension ID, on the line of the previous
//                         key's ID; the file's comments and the stores' IDs are kept
// An existing private key is reused, so re-running only refreshes the derived files.
// Pass --force to rotate the key (this changes the extension ID).
import { createHash, createPrivateKey, createPublicKey, generateKeyPairSync } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const keysDir = join(root, 'keys');
const pemPath = join(keysDir, 'dev-key.pem');
const force = process.argv.includes('--force');

mkdirSync(keysDir, { recursive: true });
let privateKey;
if (existsSync(pemPath) && !force) {
  privateKey = createPrivateKey(readFileSync(pemPath));
} else {
  ({ privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048, publicExponent: 0x10001 }));
  writeFileSync(pemPath, privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
}

/** Chromium's extension ID of a public key: its SHA-256, first 32 hex digits as letters a-p. */
const chromiumId = (spkiDer) =>
  [...createHash('sha256').update(spkiDer).digest('hex').slice(0, 32)].map((c) => String.fromCharCode(97 + parseInt(c, 16))).join('');

const publicKeyPath = join(keysDir, 'public-key.txt');
const idPath = join(root, 'EXTENSION_ID.txt');
const oldId = existsSync(publicKeyPath) ? chromiumId(Buffer.from(readFileSync(publicKeyPath, 'utf8').trim(), 'base64')) : undefined;
const spki = createPublicKey(privateKey).export({ type: 'spki', format: 'der' });
const id = chromiumId(spki);

// EXTENSION_ID.txt also lists the stores' IDs (one per line, "#" comments): only the line
// of this key's previous ID changes; without one, the ID is added at the end.
const lines = existsSync(idPath) ? readFileSync(idPath, 'utf8').replace(/\r\n/g, '\n').replace(/\n$/, '').split('\n') : [];
const at = lines.findIndex((l) => l.trim() === oldId || l.trim() === id);
if (at >= 0) lines[at] = id;
else lines.push(id);

writeFileSync(publicKeyPath, spki.toString('base64') + '\n');
writeFileSync(idPath, lines.join('\n') + '\n');
console.log(id);
