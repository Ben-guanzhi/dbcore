#!/usr/bin/env bash
# Start a 达梦 DM8 instance for the live contract tests (W14).
#
#   ./scripts/dm8-up.sh --image <ref>     pull/run an image
#   ./scripts/dm8-up.sh --tar <file>.tar  docker load a vendor tar, then run
#
# Why a script: DM8 is not on Docker Hub under the name people expect, the vendor
# ships it as a `docker load`-able tar, and the exact set of env vars decides
# whether the server comes up. Getting any of that wrong wastes a 1.5 GB pull and
# ten minutes of confusion, so it lives in one place with the reasoning attached.
#
# After it starts, run the live suites:
#   DBCORE_TEST_DM_DSN='dm://SYSDBA:SYSDBA@localhost:5236?schema=SYSDBA' \
#     go test ./dm/ -run TestDMLive -v
set -euo pipefail

NAME=dm8
PORT=5236
PASSWORD=${DM8_SYSDBA_PWD:-SYSDBA001}   # DM8 requires 9-48 chars; a 6-char password makes initdb fail with "password must between 9 and 48" (found the hard way)
IMAGE=""
TAR=""

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --image) IMAGE="${2:-}"; shift 2 ;;
    --tar)   TAR="${2:-}"; shift 2 ;;
    --port)  PORT="${2:-}"; shift 2 ;;
    --name)  NAME="${2:-}"; shift 2 ;;
    -h|--help) usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage 1 ;;
  esac
done

if [ -z "$IMAGE" ] && [ -z "$TAR" ]; then
  echo "error: give --image <ref> or --tar <file>" >&2
  usage 1
fi

command -v docker >/dev/null || { echo "error: docker not found" >&2; exit 1; }

if [ -n "$TAR" ]; then
  [ -f "$TAR" ] || { echo "error: no such file: $TAR" >&2; exit 1; }
  echo "== docker load $TAR =="
  # The load output names the image; parse it rather than guessing a tag.
  load_out=$(docker load -i "$TAR")
  echo "$load_out"
  IMAGE=$(printf '%s\n' "$load_out" | sed -n 's/^Loaded image: //p' | head -n1)
  if [ -z "$IMAGE" ]; then
    IMAGE=$(printf '%s\n' "$load_out" | sed -n 's/^Loaded image ID: //p' | head -n1)
  fi
  [ -n "$IMAGE" ] || { echo "error: could not determine the loaded image name" >&2; exit 1; }
else
  # A loaded tar leaves the image local with no registry to pull from; pulling it
  # would send the request to a mirror that has it on its allowlist-denied list.
  if docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "== using the local image $IMAGE =="
  else
    echo "== docker pull $IMAGE =="
    docker pull "$IMAGE"
  fi
fi

# A stale container from a previous attempt would hold the port and make the new
# server look like it started when it did not.
if docker ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
  echo "== removing the previous container $NAME =="
  docker rm -f "$NAME" >/dev/null
fi

echo "== docker run $NAME on :$PORT =="
# PAGE_SIZE=16: the default in DM's own images; ours must match the vendor's
# docker docs or the instance refuses to initialise.
# CASE_SENSITIVE=1: DM8's Oracle-like behaviour, which is what sqltext.DM models.
docker run -d --name "$NAME" \
  -p "$PORT:5236" \
  -e SYSDBA_PWD="$PASSWORD" \
  -e PAGE_SIZE=16 \
  -e CASE_SENSITIVE=1 \
  -e INSTANCE_NAME=DM8 \
  "$IMAGE" >/dev/null

echo -n '== waiting for the server'
ready=0
for _ in $(seq 1 60); do
  sleep 2
  printf '.'
  # The image logs this line once the instance is open; a TCP check alone would
  # pass while the instance is still initialising.
  if docker logs "$NAME" 2>&1 | grep -qE 'SYSTEM IS READY|System is ready|instance is open'; then
    ready=1
    break
  fi
  if ! docker ps --format '{{.Names}}' | grep -qx "$NAME"; then
    echo
    echo 'error: the container exited; logs follow' >&2
    docker logs --tail 40 "$NAME" >&2 || true
    exit 1
  fi
done
echo

if [ "$ready" -ne 1 ]; then
  echo 'warning: never saw the ready line; the port may still work. Logs:' >&2
  docker logs --tail 30 "$NAME" >&2 || true
fi

cat <<EOF

== DM8 is up ==
  container : $NAME
  address   : localhost:$PORT
  user      : SYSDBA
  password  : $PASSWORD
  dsn       : dm://SYSDBA:$PASSWORD@localhost:$PORT?schema=SYSDBA

Run the live suites:

  DBCORE_TEST_DM_DSN='dm://SYSDBA:$PASSWORD@localhost:$PORT?schema=SYSDBA' \\
    go test ./dm/ -run TestDMLive -v

Stop it with:

  docker rm -f $NAME
EOF
