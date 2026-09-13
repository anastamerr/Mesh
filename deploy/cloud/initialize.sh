#!/bin/sh
set -eu

umask 077
command -v openssl >/dev/null 2>&1 || {
  echo "openssl is required to generate Mesh deployment secrets." >&2
  exit 1
}

mkdir -p secrets
for name in postgres_password database_url admin_key relay_key; do
  if [ -e "secrets/$name" ]; then
    echo "secrets/$name already exists; no files were changed." >&2
    exit 1
  fi
done

database_password=$(openssl rand -hex 32)
admin_key=$(openssl rand -hex 32)
relay_key=$(openssl rand -hex 32)

printf '%s\n' "$database_password" > secrets/postgres_password
printf 'postgresql://mesh:%s@postgres:5432/mesh\n' "$database_password" > secrets/database_url
printf '%s\n' "$admin_key" > secrets/admin_key
printf '%s\n' "$relay_key" > secrets/relay_key

if [ ! -e .env ]; then
  cp .env.example .env
fi

echo "Generated private database, operator, and relay credentials."
echo "Edit .env and add the relay certificate files described in secrets/README.md."
