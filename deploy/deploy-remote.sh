#!/usr/bin/env bash
set -euo pipefail

readonly host="192.168.1.21"
readonly port="59999"
readonly user="root"
readonly container="cx-ai-relay"
readonly remote_dir="/opt/cx-ai-relay"
readonly known_hosts="${HOME}/.ssh/known_hosts"
readonly default_key="${HOME}/.ssh/cxerp-deploy.key"
readonly key="${CXAI_DEPLOY_KEY:-${default_key}}"

: "${IMAGE_ARCHIVE:?IMAGE_ARCHIVE is required}"
: "${RELEASE_SHA:?RELEASE_SHA is required}"

if [[ ! -f "${IMAGE_ARCHIVE}" ]]; then
  echo "Image archive not found: ${IMAGE_ARCHIVE}" >&2
  exit 1
fi
if [[ ! -r "${key}" ]]; then
  echo "Deployment SSH key is not readable: ${key}" >&2
  exit 1
fi
if [[ ! -r "${known_hosts}" ]] || ! ssh-keygen -F "[${host}]:${port}" -f "${known_hosts}" >/dev/null; then
  echo "Strict known_hosts entry is missing for [${host}]:${port}" >&2
  exit 1
fi

readonly ssh_options=(-i "${key}" -p "${port}" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="${known_hosts}")
readonly archive_name="cx-ai-relay-${RELEASE_SHA}.tar.gz"
readonly remote_archive="${remote_dir}/${archive_name}"

ssh "${ssh_options[@]}" "${user}@${host}" "mkdir -p '${remote_dir}/releases' && test -s '${remote_dir}/.env' && command -v docker >/dev/null && command -v curl >/dev/null"
scp -i "${key}" -P "${port}" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="${known_hosts}" \
  "${IMAGE_ARCHIVE}" "${user}@${host}:${remote_archive}"

ssh "${ssh_options[@]}" "${user}@${host}" bash -s -- "${RELEASE_SHA}" "${archive_name}" "${container}" "${remote_dir}" <<'REMOTE_SCRIPT'
set -euo pipefail
release_sha="$1"
archive_name="$2"
container="$3"
remote_dir="$4"
archive="${remote_dir}/${archive_name}"
config="${remote_dir}/.env"
candidate="${container}-candidate"
previous="${container}-previous"

test -s "${config}"
for key in DATABASE_DSN ENCRYPTION_KEY JWT_HS256_SECRET JWT_ISSUER JWT_AUDIENCE; do
  grep -Eq "^[[:space:]]*${key}[[:space:]]*=[[:space:]]*[^[:space:]#]" "${config}" || {
    echo "Required setting is missing from ${config}: ${key}" >&2
    exit 1
  }
done
gzip -t "${archive}"
docker load --input "${archive}"
image="cx-ai-relay:${release_sha}"
docker image inspect "${image}" >/dev/null

docker rm -f "${candidate}" >/dev/null 2>&1 || true
docker rm -f "${previous}" >/dev/null 2>&1 || true
old_image=""
if docker container inspect "${container}" >/dev/null 2>&1; then
  old_image="$(docker inspect --format '{{.Config.Image}}' "${container}")"
  docker stop "${container}" >/dev/null
  docker rename "${container}" "${previous}"
fi

if ! docker run -d --name "${candidate}" --restart unless-stopped --network host --env-file "${config}" "${image}" >/dev/null; then
  if [[ -n "${old_image}" ]]; then
    docker rename "${previous}" "${container}"
    docker start "${container}" >/dev/null
  fi
  exit 1
fi
healthy=false
for attempt in {1..12}; do
  if curl --fail --silent --max-time 5 http://127.0.0.1:8088/healthz >/dev/null; then
    healthy=true
    break
  fi
  sleep 5
done
if [[ "${healthy}" != true ]]; then
  docker logs --tail 80 "${candidate}" >&2 || true
  docker rm -f "${candidate}" >/dev/null
  if [[ -n "${old_image}" ]]; then
    docker rename "${previous}" "${container}"
    docker start "${container}" >/dev/null
  fi
  exit 1
fi

docker rename "${candidate}" "${container}"
if [[ -n "${old_image}" ]]; then
  docker rm -f "${previous}" >/dev/null
fi
rm -f "${archive}"
printf 'Deployed AI relay %s as %s\n' "${release_sha}" "${container}"
REMOTE_SCRIPT
