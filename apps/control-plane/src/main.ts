import { createApp } from './app';
import { loadEnvironment, readConfig } from './config';

async function main() {
  loadEnvironment();
  const config = readConfig();
  const app = await createApp(config);
  await app.listen(config.port, config.host);
  console.info(`Mesh control plane listening on ${config.host}:${config.port}`);
}

main().catch(() => {
  console.error('Control plane startup failed. Check configuration and port availability.');
  process.exitCode = 1;
});
