# syntax=docker/dockerfile:1
FROM node:22.18-bookworm-slim AS build
WORKDIR /app
COPY package.json package-lock.json ./
COPY apps/control-plane/package.json apps/control-plane/package.json
RUN npm ci
COPY apps/control-plane/ apps/control-plane/
RUN npm run build --workspace @mesh/control-plane

FROM node:22.18-bookworm-slim
ENV NODE_ENV=production
WORKDIR /app
COPY package.json package-lock.json ./
COPY apps/control-plane/package.json apps/control-plane/package.json
RUN npm ci --omit=dev && npm cache clean --force
COPY --from=build /app/apps/control-plane/dist apps/control-plane/dist
COPY apps/control-plane/migrations apps/control-plane/migrations
USER node
WORKDIR /app/apps/control-plane
CMD ["sh", "-c", "node dist/database/migrate.js && exec node dist/main.js"]
