FROM node:22.18.0-bookworm-slim AS build
WORKDIR /app
COPY package.json package-lock.json ./
COPY apps/control-plane/package.json apps/control-plane/package.json
RUN npm ci
COPY apps/control-plane/tsconfig.json apps/control-plane/tsconfig.build.json apps/control-plane/
COPY apps/control-plane/src apps/control-plane/src
COPY apps/control-plane/migrations apps/control-plane/migrations
RUN npm run build --workspace @mesh/control-plane \
    && npm prune --omit=dev

FROM node:22.18.0-bookworm-slim
ENV NODE_ENV=production
WORKDIR /app
COPY --from=build --chown=node:node /app/package.json /app/package-lock.json ./
COPY --from=build --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/apps/control-plane/package.json apps/control-plane/package.json
COPY --from=build --chown=node:node /app/apps/control-plane/dist apps/control-plane/dist
COPY --from=build --chown=node:node /app/apps/control-plane/migrations apps/control-plane/migrations
USER node
CMD ["node", "apps/control-plane/dist/main.js"]
