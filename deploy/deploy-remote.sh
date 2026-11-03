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
: "${DATABASE_DSN:?DATABASE_DSN is required}"

readonly production_dsn_suffix='@tcp(172.18.16.101:59997)/cx_ec?charset=utf8mb4&parseTime=true'
if [[ "${DATABASE_DSN}" != *"${production_dsn_suffix}" || "${DATABASE_DSN}" == *$'\n'* || "${DATABASE_DSN}" == *$'\r'* ]]; then
  echo "DATABASE_DSN must target the configured production cx_ec database" >&2
  exit 1
fi

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
readonly remote_dsn="${remote_dir}/.database-dsn-${RELEASE_SHA}"
readonly remote_dsn_upload="${remote_dsn}.upload"

ssh "${ssh_options[@]}" "${user}@${host}" "mkdir -p '${remote_dir}/releases' && test -s '${remote_dir}/.env' && command -v docker >/dev/null && command -v curl >/dev/null"
scp -i "${key}" -P "${port}" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="${known_hosts}" \
  "${IMAGE_ARCHIVE}" "${user}@${host}:${remote_archive}"
if ! printf '%s\n' "${DATABASE_DSN}" | ssh "${ssh_options[@]}" "${user}@${host}" \
  "umask 077; rm -f -- '${remote_dsn_upload}'; cat > '${remote_dsn_upload}' && mv -f -- '${remote_dsn_upload}' '${remote_dsn}'"; then
  ssh "${ssh_options[@]}" "${user}@${host}" "rm -f -- '${remote_dsn_upload}' '${remote_dsn}'" || true
  exit 1
fi

ssh "${ssh_options[@]}" "${user}@${host}" bash -s -- "${RELEASE_SHA}" "${archive_name}" "${container}" "${remote_dir}" <<'REMOTE_SCRIPT'
set -euo pipefail
release_sha="$1"
archive_name="$2"
container="$3"
remote_dir="$4"
archive="${remote_dir}/${archive_name}"
config="${remote_dir}/.env"
dsn_file="${remote_dir}/.database-dsn-${release_sha}"
candidate_config="${remote_dir}/.env-${release_sha}.candidate"
config_backup="${remote_dir}/.env-${release_sha}.previous"
config_backup_tmp="${remote_dir}/.env-${release_sha}.previous.upload"
candidate="${container}-candidate"
previous="${container}-previous"
deployment_committed=false
old_stopped=false
old_renamed=false
old_image=""

cleanup() {
  local cleanup_error=0
  if [[ "${deployment_committed}" != true ]]; then
    docker rm -f "${candidate}" >/dev/null 2>&1 || true
    if [[ -f "${config_backup}" ]] && ! mv -f -- "${config_backup}" "${config}"; then
      cleanup_error=1
    fi
    if [[ "${old_renamed}" == true ]] && docker container inspect "${previous}" >/dev/null 2>&1; then
      if docker rename "${previous}" "${container}" && docker start "${container}" >/dev/null; then
        old_renamed=false
      else
        cleanup_error=1
      fi
    elif [[ "${old_stopped}" == true ]] && docker container inspect "${container}" >/dev/null 2>&1; then
      if docker start "${container}" >/dev/null; then
        old_stopped=false
      else
        cleanup_error=1
      fi
    fi
  fi
  rm -f -- "${dsn_file}" "${candidate_config}" "${config_backup_tmp}" || cleanup_error=1
  if [[ "${deployment_committed}" == true ]]; then
    rm -f -- "${config_backup}" || cleanup_error=1
  elif [[ -f "${config_backup}" ]]; then
    cleanup_error=1
  fi
  return "${cleanup_error}"
}
trap cleanup EXIT

test -s "${config}"
for key in DATABASE_DSN ENCRYPTION_KEY JWT_HS256_SECRET JWT_ISSUER JWT_AUDIENCE; do
  grep -Eq "^[[:space:]]*${key}[[:space:]]*=[[:space:]]*[^[:space:]#]" "${config}" || {
    echo "Required setting is missing from ${config}: ${key}" >&2
    exit 1
  }
done
test -s "${dsn_file}"
test "$(wc -l < "${dsn_file}")" -eq 1
test ! -e "${config_backup}"
test ! -e "${config_backup_tmp}"
database_dsn="$(cat "${dsn_file}")"
[[ "${database_dsn}" == *@tcp\(172.18.16.101:59997\)/cx_ec\?charset=utf8mb4\&parseTime=true ]]
replacement_count=0
umask 077
while IFS= read -r line || [[ -n "${line}" ]]; do
  if [[ "${line}" == DATABASE_DSN=* ]]; then
    replacement_count=$((replacement_count + 1))
    printf 'DATABASE_DSN=%s\n' "${database_dsn}"
  else
    printf '%s\n' "${line}"
  fi
done < "${config}" > "${candidate_config}"
test "${replacement_count}" -eq 1
chmod 600 "${candidate_config}"
gzip -t "${archive}"
docker load --input "${archive}"
image="cx-ai-relay:${release_sha}"
docker image inspect "${image}" >/dev/null

docker rm -f "${candidate}" >/dev/null 2>&1 || true
docker rm -f "${previous}" >/dev/null 2>&1 || true
if docker container inspect "${container}" >/dev/null 2>&1; then
  old_image="$(docker inspect --format '{{.Config.Image}}' "${container}")"
  docker stop "${container}" >/dev/null
  old_stopped=true
  docker rename "${container}" "${previous}"
  old_stopped=false
  old_renamed=true
fi

if ! docker run -d --name "${candidate}" --restart unless-stopped --network host --env-file "${candidate_config}" "${image}" >/dev/null; then
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
  exit 1
fi

cp --preserve=mode,ownership -- "${config}" "${config_backup_tmp}"
chmod 600 "${config_backup_tmp}"
mv -f -- "${config_backup_tmp}" "${config_backup}"
mv -f -- "${candidate_config}" "${config}"
if ! docker rename "${candidate}" "${container}"; then
  exit 1
fi
deployment_committed=true
if [[ -n "${old_image}" ]]; then
  docker rm -f "${previous}" >/dev/null
fi
rm -f "${archive}"
rm -f "${config_backup}"
printf 'Deployed AI relay %s as %s\n' "${release_sha}" "${container}"
REMOTE_SCRIPT
