#!/bin/sh
set -eu
umask 077

UID=${UID:-1337}
GID=${GID:-$UID}
mkdir -p /data
cd /data
chmod 700 /data

if [ ! -f config.yaml ]; then
    /usr/bin/mautrix-groupme -c /data/config.yaml -e
    chown -R "$UID:$GID" /data
    echo "Created /data/config.yaml. Configure the bridge and restart the container."
    exit 0
fi

if [ ! -f registration.yaml ] && ! yq -e '.appservice.as_token != null and .appservice.as_token != "" and .appservice.as_token != "This value is generated when generating the registration"' config.yaml >/dev/null; then
    /usr/bin/mautrix-groupme -g -c /data/config.yaml -r /data/registration.yaml
    chown -R "$UID:$GID" /data
    echo "Created /data/registration.yaml. Register it with your homeserver and restart the container."
    exit 0
fi

chmod 600 config.yaml
if [ -f registration.yaml ]; then chmod 600 registration.yaml; fi
chown -R "$UID:$GID" /data
exec su-exec "$UID:$GID" /usr/bin/mautrix-groupme "$@"
