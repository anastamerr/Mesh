import { createApp } from './app';
import { readConfig } from './config';

async function main() {
  const config = readConfig();
  const app = await createApp(config);
  await app.listen(config.port, config.host);
  console.info(`Mesh control plane listening on ${config.host}:${config.port}`);
}

main().catch(() => {
  console.error('Control plane startup failed. Check configuration and port availability.');
  process.exitCode = 1;
});
