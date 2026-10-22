#!/usr/bin/env bash
# Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
# See LICENSE.txt for license information.

set -euo pipefail

if [[ $# != 1 ]]; then
    echo "Usage: $0 /absolute/path/to/playbooks-bundle.tar.gz" >&2
    exit 2
fi

smoke_bundle=$(realpath "$1")
smoke_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
smoke_go=${GO:-go}

# Do not reuse or replace another test session's containers or database.
for smoke_container in playbooks-office-smoke-mm playbooks-office-smoke-db; do
    if docker inspect "$smoke_container" >/dev/null 2>&1; then
        echo "Container $smoke_container already exists. Remove your previous smoke environment first." >&2
        exit 2
    fi
done
if docker network inspect playbooks-office-smoke >/dev/null 2>&1; then
    echo "Network playbooks-office-smoke already exists. Remove your previous smoke environment first." >&2
    exit 2
fi

smoke_tmp=$(mktemp -d /tmp/playbooks-office-smoke.XXXXXX)
smoke_created_network=0
smoke_created_db=0
smoke_created_mm=0
cleanup() {
    if [[ ${KEEP_OFFICE_SMOKE:-0} == 1 ]]; then
        echo "Kept disposable smoke host at http://localhost:18065 and build files in $smoke_tmp"
        return
    fi
    if [[ $smoke_created_mm == 1 ]]; then
        docker rm -fv playbooks-office-smoke-mm >/dev/null
    fi
    if [[ $smoke_created_db == 1 ]]; then
        docker rm -fv playbooks-office-smoke-db >/dev/null
    fi
    if [[ $smoke_created_network == 1 ]]; then
        docker network rm playbooks-office-smoke >/dev/null
    fi
    rm -rf "$smoke_tmp"
}
trap cleanup EXIT

cd "$smoke_root"
GOCACHE=${GOCACHE:-$smoke_tmp/go-cache} CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    "$smoke_go" build -o "$smoke_tmp/caller" ./tests/office-smoke/caller

docker network create playbooks-office-smoke >/dev/null
smoke_created_network=1
docker run -d --name playbooks-office-smoke-db --network playbooks-office-smoke \
    -e POSTGRES_USER=mmuser -e POSTGRES_PASSWORD=mostest -e POSTGRES_DB=office_smoke \
    postgres:15 >/dev/null
smoke_created_db=1

docker run -d --name playbooks-office-smoke-mm --network playbooks-office-smoke \
    -p 127.0.0.1:18065:8065 \
    -e MM_SQLSETTINGS_DRIVERNAME=postgres \
    -e 'MM_SQLSETTINGS_DATASOURCE=postgres://mmuser:mostest@playbooks-office-smoke-db:5432/office_smoke?sslmode=disable&connect_timeout=10' \
    -e MM_SERVICESETTINGS_SITEURL=http://localhost:18065 \
    -e MM_SERVICESETTINGS_ENABLEDEVELOPER=true \
    -e MM_SERVICESETTINGS_ENABLELOCALMODE=true \
    -e MM_PLUGINSETTINGS_ENABLEUPLOADS=true \
    -e MM_PLUGINSETTINGS_ENABLEMARKETPLACE=false \
    -e MM_PLUGINSETTINGS_AUTOMATICPREPACKAGEDPLUGINS=false \
    -e MM_PLUGINSETTINGS_REQUIREPLUGINSIGNATURE=false \
    -e MM_LOGSETTINGS_CONSOLELEVEL=ERROR \
    -e MM_EMAILSETTINGS_REQUIREEMAILVERIFICATION=false \
    -e MM_FILESETTINGS_MAXFILESIZE=1048576000 \
    mattermost/mattermost-team-edition:12.0.0 >/dev/null
smoke_created_mm=1

smoke_ready=0
for ((smoke_attempt = 0; smoke_attempt < 90; smoke_attempt++)); do
    if curl -fsS http://127.0.0.1:18065/api/v4/system/ping >/dev/null; then
        smoke_ready=1
        break
    fi
    sleep 1
done
if [[ $smoke_ready == 0 ]]; then
    docker logs --tail 100 playbooks-office-smoke-mm >&2
    exit 1
fi

docker exec playbooks-office-smoke-mm /mattermost/bin/mmctl --local user create \
    --email office-smoke-admin@example.com --username office-smoke-admin \
    --password 'OfficeSmoke-Only123!' --system-admin

python3 tests/office-smoke/verify.py --bundle "$smoke_bundle" --caller-binary "$smoke_tmp/caller"
