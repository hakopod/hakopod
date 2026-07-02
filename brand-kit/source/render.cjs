// Authoring only. Install sharp in your tool environment, or set HAKOPOD_SHARP_PATH.
// No JavaScript or image library is needed to use the delivered SVG files.
const fs = require('node:fs/promises');
const path = require('node:path');
const sharp = require(process.env.HAKOPOD_SHARP_PATH || 'sharp');
sharp.cache(false);
sharp.concurrency(1);
(async () => {
  const root = path.resolve(process.argv[2] || '.');
  const jobs = JSON.parse(await fs.readFile(path.join(root, 'source/render-jobs.json'), 'utf8'));
  for (const job of jobs) {
    await sharp(path.join(root, job.source), {limitInputPixels: 16_000_000})
      .resize({width:job.width || undefined,height:job.height || undefined,fit:'contain'})
      .png({compressionLevel:9,adaptiveFiltering:true})
      .toFile(path.join(root,job.target));
  }
  // Preserve the exact pixel-aligned 16, 32, and 48px exports in a multi-size ICO.
  const sizes = [16, 32, 48];
  const frames = [];
  const directory = Buffer.alloc(6 + sizes.length * 16);
  directory.writeUInt16LE(1, 2);
  directory.writeUInt16LE(sizes.length, 4);
  let offset = directory.length;
  for (const [index, size] of sizes.entries()) {
    const frame = await fs.readFile(path.join(root, `icons/favicon-${size}.png`));
    const entry = 6 + index * 16;
    directory[entry] = size;
    directory[entry + 1] = size;
    directory.writeUInt16LE(1, entry + 4);
    directory.writeUInt16LE(32, entry + 6);
    directory.writeUInt32LE(frame.length, entry + 8);
    directory.writeUInt32LE(offset, entry + 12);
    frames.push(frame);
    offset += frame.length;
  }
  await fs.writeFile(path.join(root, 'icons/favicon.ico'), Buffer.concat([directory, ...frames]));
  console.log(`Rendered ${jobs.length} PNG assets sequentially and a three-size favicon ICO.`);
})().catch(error => { console.error(error); process.exitCode=1; });
