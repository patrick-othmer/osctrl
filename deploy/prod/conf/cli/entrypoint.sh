#!/bin/sh
# osctrl bootstrap — production compose stack.
#
# One-shot initialization, idempotent: creates the osctrl environment,
# tunes intervals, seeds a scheduled query and posture profile, and creates
# the administrator. Exits 0 on success so compose can gate osctrl-tls and
# osctrl-api on `service_completed_successfully`.

set -eu

CLI=/opt/osctrl/bin/osctrl-cli

ENV_NAME="${ENV_NAME:-prod}"
HOST="${HOST:?HOST (public hostname of the osquery endpoint) is required}"
CERT_FILE="${CERT_FILE:-/opt/osctrl/config/osctrl.crt}"
OSCTRL_USER="${OSCTRL_USER:?OSCTRL_USER is required}"
OSCTRL_PASS="${OSCTRL_PASS:?OSCTRL_PASS is required}"
LOGGING_INTERVAL="${LOGGING_INTERVAL:-90}"
CONFIG_INTERVAL="${CONFIG_INTERVAL:-60}"
QUERY_INTERVAL="${QUERY_INTERVAL:-30}"
POSTURE_PROFILE="${POSTURE_PROFILE:-linux-server}"
POSTURE_INTERVAL="${POSTURE_INTERVAL:-75}"
POSTURE_QUERY_PREFIX="${POSTURE_QUERY_PREFIX:-osctrl:posture:}"
WAIT="${WAIT:-5}"

if [ -n "${OSCTRL_PASS_FILE:-}" ]; then
  OSCTRL_PASS="$(cat "${OSCTRL_PASS_FILE}")"
fi

if [ ! -f "${CERT_FILE}" ]; then
  echo "ERROR: certificate ${CERT_FILE} not found (mount ./secrets/osctrl.crt)" >&2
  exit 1
fi

######################################### Wait until DB is up ##################################
retries=0
until "${CLI}" check-db; do
  retries=$((retries + 1))
  if [ "${retries}" -ge 60 ]; then
    echo "ERROR: database did not become ready after ${retries} attempts" >&2
    exit 1
  fi
  echo "DB is not ready, retrying in ${WAIT}s..."
  sleep "${WAIT}"
done

######################################### Create environment ###################################
if "${CLI}" --db env add \
  --name "${ENV_NAME}" \
  --hostname "${HOST}" \
  --certificate "${CERT_FILE}"; then
  echo "Created environment ${ENV_NAME}"
else
  echo "Environment ${ENV_NAME} already exists"
fi

######################################### Adjust intervals #####################################
if "${CLI}" --db env update \
  --name "${ENV_NAME}" \
  --logging "${LOGGING_INTERVAL}" \
  --config "${CONFIG_INTERVAL}" \
  --query "${QUERY_INTERVAL}"; then
  echo "Adjusted intervals for ${ENV_NAME}"
else
  echo "WARNING: could not adjust intervals for ${ENV_NAME}"
fi

######################################### Add scheduled query ##################################
if "${CLI}" --db env add-scheduled-query \
  --name "${ENV_NAME}" \
  --query-name "uptime" \
  --query "SELECT * FROM uptime;" \
  --interval "60"; then
  echo "Added query to schedule in ${ENV_NAME}"
else
  echo "Scheduled query already exists in ${ENV_NAME}"
fi

######################################### Add posture checks ###################################
if "${CLI}" --db env add-posture-queries \
  --name "${ENV_NAME}" \
  --profile "${POSTURE_PROFILE}" \
  --interval "${POSTURE_INTERVAL}" \
  --prefix "${POSTURE_QUERY_PREFIX}"; then
  echo "Added posture profile ${POSTURE_PROFILE} to schedule in ${ENV_NAME}"
else
  echo "Posture profile already exists in ${ENV_NAME}"
fi

######################################### Create admin user ###################################
if "${CLI}" --db user add \
  --admin \
  --username "${OSCTRL_USER}" \
  --password "${OSCTRL_PASS}" \
  --environment "${ENV_NAME}" \
  --fullname "${OSCTRL_USER}"; then
  echo "Created ${OSCTRL_USER} user"
else
  echo "The user ${OSCTRL_USER} exists"
fi

echo "The environment ${ENV_NAME} is ready"
echo "
##############################################################################
#                Successfully created an osctrl user and env
#
# osctrl admin user: ${OSCTRL_USER}
# osctrl env name:   ${ENV_NAME}
##############################################################################
"
